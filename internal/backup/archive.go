package backup

// Names inside the zip.
const (
	DBFile       = "kipple.db"
	OPMLFile     = "feeds.opml"
	SettingsFile = "settings.json"
	ReadmeFile   = "RESTORE.txt"
	ManifestFile = "manifest.json"

	// ManifestFormat is the manifest layout version.
	ManifestFormat = 1

	// MaxEntries is the most entries a backup zip may hold before it is refused
	// unread: a real one has len(backupFiles).
	MaxEntries = 10
)

// backupFiles is every file a backup zip holds.
var backupFiles = []string{DBFile, OPMLFile, SettingsFile, ReadmeFile, ManifestFile}

// FileEntry is one file's size and checksum in the manifest.
type FileEntry struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Manifest is manifest.json. Files lists every file except the manifest itself.
type Manifest struct {
	Format        int         `json:"format"`
	App           string      `json:"app"`
	KippleVersion string      `json:"kipple_version"`
	SchemaVersion int         `json:"schema_version"`
	ApplicationID int         `json:"application_id"`
	CreatedAt     string      `json:"created_at"`
	DBSHA256      string      `json:"db_sha256"`
	DBBytes       int64       `json:"db_bytes"`
	Feeds         int64       `json:"feeds"`
	Items         int64       `json:"items"`
	Starred       int64       `json:"starred"`
	Files         []FileEntry `json:"files"`
}

const restoreText = `Kipple backup
=============

This zip holds a consistent copy of your Kipple database (kipple.db), your
subscriptions as OPML (feeds.opml), a readable copy of your settings
(settings.json) and manifest.json with a SHA-256 for every file.

kipple.db is the only file a full restore uses. The rest is for humans:
feeds.opml imports into any feed reader.

To restore on a new Kipple, open it in a browser before creating an account,
choose "Restore from a backup" and pick this file. It brings back
your account, settings, feeds and history; Kipple restarts to apply it, then
you sign in with this backup's account.
The public URL, allowed host names and trusted proxies are not restored: they
describe the old server, so set them again for the new one.

To restore over an existing library, use the command line: stop Kipple, run
    kipple restore <this file> --yes
(with Docker, from where you keep this file, with the kipple service stopped:
    docker compose run --rm -T --no-deps kipple restore - --yes < <this file>)
and start Kipple again. The current database is moved to backup/pre-restore-*
first.

Either way every web session is signed out, and images and icons are
downloaded again when they are first shown.

This file contains credentials. It holds your web and app password
hashes, the account secret that signs sync app sign-ins (anyone with this
file can sign in from a sync app as you until you generate a new app password), and any
feed logins in plain text. Store it privately, where nobody else can open it.
`
