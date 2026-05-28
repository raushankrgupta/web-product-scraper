package myntra_scraper

import (
	"fmt"
	"sync"
)

// portManager handles port allocation for concurrent ChromeDriver
// instances spawned by Selenium fallbacks. Each package needs its own
// allocator so the Myntra scraper and the other scrapers don't collide
// on the same TCP port (or on the same in-memory "in use" bitmap).
type portManager struct {
	basePort  int
	portRange int
	portMap   map[int]bool
	mutex     sync.Mutex
}

var (
	globalPortManager *portManager
	once              sync.Once
)

// initPortManager initialises the package-local port manager exactly once.
// The base port and range are deliberately disjoint from scrapers/base's
// 4444-4459 range to prevent the two packages from racing on the same
// ChromeDriver listen port when scrapes run concurrently.
func initPortManager(basePort, portRange int) {
	once.Do(func() {
		globalPortManager = newPortManager(basePort, portRange)
	})
}

func newPortManager(basePort, portRange int) *portManager {
	portMap := make(map[int]bool)
	for i := 0; i < portRange; i++ {
		portMap[basePort+i] = false
	}

	return &portManager{
		basePort:  basePort,
		portRange: portRange,
		portMap:   portMap,
		mutex:     sync.Mutex{},
	}
}

func (pm *portManager) GetPort() (int, error) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	for i := 0; i < pm.portRange; i++ {
		port := pm.basePort + i
		if !pm.portMap[port] {
			pm.portMap[port] = true
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", pm.basePort, pm.basePort+pm.portRange-1)
}

func (pm *portManager) ReleasePort(port int) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	pm.portMap[port] = false
}
