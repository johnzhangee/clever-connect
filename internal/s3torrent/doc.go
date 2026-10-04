// Package s3torrent implements the direct-to-S3 torrent storage backend.
//
// When the StorageConfig.DirectS3Enabled flag is on, newly added torrents are
// routed (per info hash) through this package's dispatching storage.Client:
// their piece data is written straight into object storage and no torrent
// byte ever lands on the local container disk.
//
// The design, in one paragraph: each torrent maps to ONE final S3 object
// ("<prefix>torrents/<infohash>.data") assembled from 64 MiB multipart
// parts. Piece blocks arriving from the swarm are copied by WriteAt (which
// runs under the torrent client's lock and therefore never blocks — it
// refuses with an error when RAM is exhausted and the client re-requests)
// into the buffers of the parts they overlap. When a piece passes its hash
// check the client calls MarkComplete; a part whose every overlapping piece
// is verified becomes uploadable and is PUT as its own staged object
// ("<prefix>torrents/<infohash>.parts/NNNNNNNNNN"). Once every piece is
// verified and every part object stored, the final object is assembled with
// server-side CopyObjectPart calls (zero data flow through this host),
// committed via CompleteMultipartUpload, and the staged part objects are
// deleted. Torrents that fit inside a single part skip multipart entirely.
//
// Reads never touch the disk either: ReadAt serves from a resident part
// buffer, else from the staged part object in the bucket, else (after
// commit) from the final data object.
//
// Resume: models.TorrentS3Upload persists the upload state (multipart ID,
// which part objects exist, dimensions). After a restart the backend
// rebuilds its part layout from the row; pieces fully covered by stored
// part objects are reported complete to the client via Completion(), so
// they are never re-downloaded, and the multipart session is reused (or
// transparently restarted when the server expired it).
package s3torrent
