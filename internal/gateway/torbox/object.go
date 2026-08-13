// Package torbox exposes completed TorBox torrent files as strict range sources.
package torbox

import (
	"fmt"
	"regexp"
	"strconv"
)

var objectIDPattern = regexp.MustCompile(`^([1-9][0-9]*):([1-9][0-9]*)$`)

type objectID struct {
	TorrentID int64
	FileID    int64
}

func parseObjectID(value string) (objectID, error) {
	matches := objectIDPattern.FindStringSubmatch(value)
	if len(matches) != 3 {
		return objectID{}, fmt.Errorf("invalid TorBox object ID: %q", value)
	}
	torrentID, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil {
		return objectID{}, fmt.Errorf("parse TorBox torrent ID: %w", err)
	}
	fileID, err := strconv.ParseInt(matches[2], 10, 64)
	if err != nil {
		return objectID{}, fmt.Errorf("parse TorBox file ID: %w", err)
	}
	return objectID{TorrentID: torrentID, FileID: fileID}, nil
}

func (i objectID) String() string {
	return strconv.FormatInt(i.TorrentID, 10) + ":" + strconv.FormatInt(i.FileID, 10)
}
