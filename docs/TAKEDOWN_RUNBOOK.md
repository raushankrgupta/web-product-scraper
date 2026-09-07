# Takedown & Grievance Runbook

Why this exists: from app 2.4.0 users import product images from third-party
sites on their own device and store them here. The company's position is that
of an intermediary hosting user content (IT Act §79; DMCA §512 if applicable).
That position depends on acting promptly and provably on complaints. This is
the procedure. Keep it boring and keep it fast.

## Deadlines

| Trigger | Act within |
|---|---|
| Any complaint received at `GRIEVANCE_EMAIL` | Acknowledge in **24 hours** |
| Ordinary complaint | Resolve in **15 days** |
| Court order or government notice (IT Rules 2021, Rule 3(1)(d)) | Remove in **36 hours** |
| Records of removed content and the notice | Retain **180 days** |

## 1. Acknowledge
Reply from the grievance inbox: date received, a ticket reference (use the
email's Message-ID), and the timeline above. Log it in the grievance sheet
(date, complainant, what, which user/content, action, closed date).

## 2. Identify the content
Complaints reference a try-on image, a product image, or an account.

```js
// mongosh "$MONGO_URI"; use fitly
// by S3 key or presigned URL fragment
db.products.find({ image_paths: /product_uploads\/<uuid>/ })
db.wardrobe.find({ images: /product_uploads\/<uuid>/ })
// by source page
db.products.find({ source: "link_import", page_host: "brand.example" })
// by user
db.products.find({ user_id: "<id>" })
```

## 3. Remove
1. Delete the S3 objects: `utils.DeleteObjectsFromS3(ctx, keys)` — from a
   one-off `go run` or the AWS console (bucket `AWS_BUCKET_NAME`).
2. Delete the Mongo rows: `db.products.deleteOne({_id})`,
   `db.wardrobe.deleteMany({ images: { $in: [keys] } })`, and any `tryons`
   rows whose garment keys match.
3. If the complaint is about a **site/tool** rather than one image (e.g. a
   retailer asks that their domain not be openable): add the host to
   `LINK_IMPORT_BLOCKED_HOSTS` and restart. Clients pick it up within 5
   minutes (`GET /app/config` cache) and refuse to open the site.

## 4. Notify
- Complainant: what was removed and when.
- User: which item was removed and why (their Content, our Terms §5/§7).
  Second substantiated notice: warning. Third: terminate the account via the
  existing account deletion path (repeat-infringer rule, Terms §5).

## 5. Record
Export the notice, the identifiers, and the actions to the retention folder.
Keep 180 days.

## Contacts to keep current
`GRIEVANCE_OFFICER_NAME`, `GRIEVANCE_EMAIL`, `LEGAL_POSTAL_ADDRESS` in the
server environment (they render into the in-app Terms), and the same details
in `static/terms.html` (placeholders `GRIEVANCE_*`).
