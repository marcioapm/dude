package agenttools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// The image library as GET /v1/images summarises it (SUMMARY_COLUMNS in the
// control plane's routes/images.ts, which has no SQL function to share):
// the same rows, minus the Containerfile and what is derived from it, audit,
// usage, build errors, logs and the builder's queue.

type imageVersionOut struct {
	VersionID string `json:"versionId"`
	Number    int    `json:"number"`
}

type imagePublishedOut struct {
	imageVersionOut
	// The built user image by digest; written by dude-image-builder.
	Ref              *string    `json:"ref"`
	BuiltAt          *time.Time `json:"builtAt"`
	CanRunContainers bool       `json:"canRunContainers"`
}

type imagePendingOut struct {
	imageVersionOut
	State string `json:"state"`
}

type imageOut struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	ArchivedAt  *time.Time         `json:"archivedAt"`
	Default     bool               `json:"organizationDefault"`
	Published   *imagePublishedOut `json:"published"`
	Pending     *imagePendingOut   `json:"pending"`
	Draft       bool               `json:"draft"`
	Parents     []refOut           `json:"parents"`
}

type imagesOut struct {
	Section string     `json:"section"`
	Source  string     `json:"source"`
	Images  []imageOut `json:"images"`
	Next    string     `json:"next,omitempty"`
}

type imageCursor struct {
	Section  string `json:"section"`
	Org      string `json:"org"`
	Archived bool   `json:"archived"`
	Name     string `json:"name"`
	ID       string `json:"id"`
}

func decodeImageCursor(org, s string) (*imageCursor, error) {
	if s == "" {
		return nil, nil
	}
	var c imageCursor
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Section != "images" || c.Org != org || c.ID == "" {
		return nil, refuseText(settingsText("catalogue-cursor", nil))
	}
	return &c, nil
}

// imageCatalogue reads one page in one statement, ordered as the route lists
// images (archived last, by name) with id breaking ties.
func imageCatalogue(ctx context.Context, tx pgx.Tx, w wrapper, org string, in settingsIn) (*imagesOut, error) {
	limit, err := catalogueLimit(in.Limit)
	if err != nil {
		return nil, err
	}
	after, err := decodeImageCursor(org, in.Cursor)
	if err != nil {
		return nil, err
	}
	var archived *bool
	var name, id string
	if after != nil {
		archived, name, id = &after.Archived, after.Name, after.ID
	}
	rows, err := tx.Query(ctx, `SELECT i.id, i.name, i.description, i.archived_at, i.id IS NOT DISTINCT FROM o.default_image_id,
			pub.id, pub.number, pub.user_ref, pub.built_at, COALESCE(pub.can_run_containers, false),
			pend.id, pend.number, pend.state,
			EXISTS (SELECT 1 FROM image_versions d WHERE d.image_id = i.id AND d.state = 'draft'),
			(SELECT COALESCE(json_agg(json_build_object('id', pi.id, 'name', pi.name) ORDER BY pi.name), '[]')
			 FROM image_version_parents vp JOIN images pi ON pi.id = vp.parent_image_id WHERE vp.version_id = latest.id)
		FROM images i JOIN organizations o ON o.id = i.organization_id
		LEFT JOIN image_versions pub ON pub.id = i.published_version_id
		LEFT JOIN LATERAL (SELECT v.id, v.number, v.state FROM image_versions v WHERE v.image_id = i.id AND v.number IS NOT NULL
			AND v.number > COALESCE(pub.number, 0) AND v.state IN ('queued', 'building', 'pushing', 'failed')
			ORDER BY v.number DESC LIMIT 1) pend ON true
		LEFT JOIN LATERAL (SELECT v.id FROM image_versions v WHERE v.image_id = i.id
			ORDER BY (v.state = 'draft') DESC, v.number DESC NULLS LAST LIMIT 1) latest ON true
		WHERE $1::boolean IS NULL OR (i.archived_at IS NOT NULL, i.name, i.id) > ($1, $2, $3)
		ORDER BY i.archived_at IS NOT NULL, i.name, i.id LIMIT $4`, archived, name, id, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &imagesOut{Section: "images", Source: sourceOrg, Images: []imageOut{}}
	keys := []imageCursor{}
	for rows.Next() {
		var img imageOut
		var rawName, desc string
		var pubID, pendID, pendState *string
		var pubNumber, pendNumber *int
		var pub imagePublishedOut
		var parents []struct{ ID, Name string }
		if err := rows.Scan(&img.ID, &rawName, &desc, &img.ArchivedAt, &img.Default, &pubID, &pubNumber, &pub.Ref, &pub.BuiltAt,
			&pub.CanRunContainers, &pendID, &pendNumber, &pendState, &img.Draft, &parents); err != nil {
			return nil, err
		}
		img.Name, img.Description = w.text(rawName, 100), w.text(desc, 2000)
		if pubID != nil {
			pub.imageVersionOut = imageVersionOut{VersionID: *pubID, Number: *pubNumber}
			img.Published = &pub
		}
		if pendID != nil {
			img.Pending = &imagePendingOut{imageVersionOut: imageVersionOut{VersionID: *pendID, Number: *pendNumber}, State: *pendState}
		}
		img.Parents = []refOut{}
		for _, p := range parents {
			img.Parents = append(img.Parents, refOut{ID: p.ID, Name: w.text(p.Name, 100)})
		}
		keys = append(keys, imageCursor{Section: "images", Org: org, Archived: img.ArchivedAt != nil, Name: rawName, ID: img.ID})
		out.Images = append(out.Images, img)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	fetched := len(out.Images)
	if fetched > limit {
		out.Images = out.Images[:limit]
	}
	n, err := pageFit(out, len(out.Images), func(n int) { out.Images = out.Images[:n] })
	if err != nil {
		return nil, err
	}
	if n < fetched {
		raw, _ := json.Marshal(keys[n-1])
		out.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}
