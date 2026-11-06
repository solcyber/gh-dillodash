package model

// CommitChange is a commit's parents and changed files, as the commits
// API returns them.
type CommitChange struct {
	SHA     string
	Parents []string
	Files   []ChangedFile
	// Truncated is set when the commit changes more files than were read.
	Truncated bool
}

// ChangedFile is one file a commit changed: its status (added, removed,
// modified, renamed, copied, changed, unchanged), the new blob, and the
// old path of a rename.
type ChangedFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	SHA              string `json:"sha"`
}

// FileAdd is a file a commit writes: the path and its base64 contents.
type FileAdd struct {
	Path   string
	Base64 string
}
