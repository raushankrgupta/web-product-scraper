package api

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// Tutorials: the app's "how to" screen is fed by the TryOnFusion YouTube
// channel. Publishing a video there is the only step needed for it to appear
// in the app — this handler reads the channel's public Atom feed
// (youtube.com/feeds/videos.xml), which YouTube maintains for every channel
// and which needs no API key or quota.
//
// The feed is fetched at most once per TUTORIALS_CACHE_SECS and the last good
// copy is served if YouTube is unreachable, so the screen never goes blank
// because of a transient failure upstream.

// TutorialVideo is one entry as the app renders it.
type TutorialVideo struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	PublishedAt  time.Time `json:"published_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	ThumbnailURL string    `json:"thumbnail_url"`
	// URL is the watch/shorts page; EmbedURL is what the in-app player loads.
	URL      string `json:"url"`
	EmbedURL string `json:"embed_url"`
	IsShort  bool   `json:"is_short"`
	Views    int64  `json:"views"`
}

// TutorialChannel identifies the source channel for the "Subscribe" link.
type TutorialChannel struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// TutorialsResponse is what GET /tutorials returns.
type TutorialsResponse struct {
	Channel   TutorialChannel `json:"channel"`
	Videos    []TutorialVideo `json:"videos"`
	FetchedAt time.Time       `json:"fetched_at"`
	// Stale is true when the upstream fetch failed and this is the last good copy.
	Stale bool `json:"stale"`
}

// --- Atom feed shape (only the fields we read) ---

type ytFeed struct {
	Title   string    `xml:"title"`
	Entries []ytEntry `xml:"entry"`
}

type ytEntry struct {
	VideoID   string `xml:"http://www.youtube.com/xml/schemas/2015 videoId"`
	Title     string `xml:"title"`
	Published string `xml:"published"`
	Updated   string `xml:"updated"`
	Links     []struct {
		Rel  string `xml:"rel,attr"`
		Href string `xml:"href,attr"`
	} `xml:"link"`
	Group struct {
		Description string `xml:"http://search.yahoo.com/mrss/ description"`
		Thumbnail   struct {
			URL string `xml:"url,attr"`
		} `xml:"http://search.yahoo.com/mrss/ thumbnail"`
		Community struct {
			Statistics struct {
				Views int64 `xml:"views,attr"`
			} `xml:"http://search.yahoo.com/mrss/ statistics"`
		} `xml:"http://search.yahoo.com/mrss/ community"`
	} `xml:"http://search.yahoo.com/mrss/ group"`
}

// parseTutorialFeed turns the Atom XML into the app's shape. Exported to the
// test through the package; pure.
func parseTutorialFeed(data []byte, hidden map[string]bool) (title string, videos []TutorialVideo, err error) {
	var feed ytFeed
	if err := xml.Unmarshal(data, &feed); err != nil {
		return "", nil, fmt.Errorf("parse feed: %w", err)
	}
	videos = make([]TutorialVideo, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		id := strings.TrimSpace(e.VideoID)
		if id == "" || hidden[id] {
			continue
		}
		v := TutorialVideo{
			ID:           id,
			Title:        strings.TrimSpace(e.Title),
			Description:  strings.TrimSpace(e.Group.Description),
			ThumbnailURL: e.Group.Thumbnail.URL,
			Views:        e.Group.Community.Statistics.Views,
			URL:          "https://www.youtube.com/watch?v=" + id,
			// youtube-nocookie: the privacy-enhanced embed domain. Same player,
			// no tracking cookies dropped before the user presses play.
			EmbedURL: "https://www.youtube-nocookie.com/embed/" + id + "?playsinline=1&rel=0&modestbranding=1",
		}
		for _, l := range e.Links {
			if l.Rel == "alternate" && l.Href != "" {
				v.URL = l.Href
				// YouTube itself links a Short to /shorts/<id>; that is the
				// only signal the feed carries about format, and it is enough.
				if strings.Contains(l.Href, "/shorts/") {
					v.IsShort = true
				}
			}
		}
		if t, err := time.Parse(time.RFC3339, e.Published); err == nil {
			v.PublishedAt = t
		}
		if t, err := time.Parse(time.RFC3339, e.Updated); err == nil {
			v.UpdatedAt = t
		}
		if v.ThumbnailURL == "" {
			v.ThumbnailURL = "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"
		}
		videos = append(videos, v)
	}
	// Newest first, which is also how the channel page orders them.
	sort.SliceStable(videos, func(i, j int) bool { return videos[i].PublishedAt.After(videos[j].PublishedAt) })
	return strings.TrimSpace(feed.Title), videos, nil
}

// tutorialsCache is the process-wide memo of the last successful fetch.
type tutorialsCache struct {
	sync.Mutex
	resp      *TutorialsResponse
	fetchedAt time.Time
	lastTry   time.Time
}

var tutorials = &tutorialsCache{}

// fetchTutorialFeed downloads the channel feed. Uses the SSRF-safe client
// even though the host is fixed, because that is the only outbound client
// this codebase wants to see.
var fetchTutorialFeed = func(ctx context.Context, channelID string) ([]byte, error) {
	feedURL := "https://www.youtube.com/feeds/videos.xml?channel_id=" + channelID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/atom+xml, application/xml;q=0.9, */*;q=0.5")
	req.Header.Set("User-Agent", "TryOnFusion-Server/1.0 (+https://www.tryonfusion.com)")
	resp, err := utils.NewSafeHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feed returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 2<<20))
}

// currentTutorials returns the cached response, refreshing it when older than
// the TTL. A failed refresh keeps the previous copy (marked stale) and backs
// off for a minute so a YouTube outage does not turn into a request per tap.
func currentTutorials(ctx context.Context) (*TutorialsResponse, error) {
	tutorials.Lock()
	defer tutorials.Unlock()

	ttl := time.Duration(config.TutorialsCacheSecs) * time.Second
	fresh := tutorials.resp != nil && time.Since(tutorials.fetchedAt) < ttl
	recentlyFailed := time.Since(tutorials.lastTry) < time.Minute && tutorials.resp != nil && tutorials.resp.Stale
	if fresh || recentlyFailed {
		return tutorials.resp, nil
	}

	tutorials.lastTry = time.Now()
	hidden := map[string]bool{}
	for _, id := range config.TutorialsHiddenVideoIDs {
		hidden[id] = true
	}

	data, err := fetchTutorialFeed(ctx, config.YouTubeChannelID)
	var title string
	var videos []TutorialVideo
	if err == nil {
		title, videos, err = parseTutorialFeed(data, hidden)
	}
	if err != nil {
		alert.Warnf("tutorials", "youtube feed refresh failed", err, "channel", config.YouTubeChannelID)
		if tutorials.resp != nil {
			tutorials.resp.Stale = true
			return tutorials.resp, nil
		}
		return nil, err
	}

	if title == "" {
		title = "TryOnFusion"
	}
	tutorials.resp = &TutorialsResponse{
		Channel: TutorialChannel{
			ID:     config.YouTubeChannelID,
			Handle: config.YouTubeChannelHandle,
			Title:  title,
			URL:    "https://www.youtube.com/" + config.YouTubeChannelHandle,
		},
		Videos:    videos,
		FetchedAt: time.Now().UTC().Truncate(time.Second),
	}
	tutorials.fetchedAt = time.Now()
	return tutorials.resp, nil
}

// TutorialsHandler serves GET /tutorials. Public: the videos are public, and
// the screen is worth showing to guests too.
func TutorialsHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	resp, err := currentTutorials(ctx)
	if err != nil {
		utils.RespondError(w, nil, "Tutorials are unavailable right now. Please try again later.", http.StatusServiceUnavailable)
		return
	}

	data, err := json.Marshal(resp)
	if err != nil {
		utils.RespondError(w, nil, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	etag := fmt.Sprintf(`"%x"`, md5.Sum(data))
	w.Header().Set("ETag", etag)
	// Short public cache: a new upload should show up within minutes, and
	// the server-side TTL already keeps YouTube traffic to a trickle.
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(data)
	w.Write([]byte("\n"))
}
