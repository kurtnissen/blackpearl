package resolver

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
)

type releaseRank struct {
	intentMatch bool
	titlePrefix bool
	yearMatch   bool
	hasHash     bool
	seeders     int
	size        int64
	provider    string
	sourceID    string
}

func rankAndDeduplicate(request acquisition.SearchRequest, releases []acquisition.Release) []acquisition.Release {
	usable := make([]acquisition.Release, 0, len(releases))
	for _, release := range releases {
		if usableRelease(release) {
			usable = append(usable, release)
		}
	}
	sort.SliceStable(usable, func(left int, right int) bool {
		return releaseLess(rankRelease(request, usable[left]), rankRelease(request, usable[right]))
	})
	seen := make(map[string]struct{}, len(usable))
	result := make([]acquisition.Release, 0, len(usable))
	for _, release := range usable {
		key := releaseIdentity(release)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, release)
	}
	return result
}

func usableRelease(release acquisition.Release) bool {
	if release.Provider() == "" || release.SourceID() == "" || release.Title() == "" || release.Indexer() == "" || release.Size() <= 0 {
		return false
	}
	switch release.Protocol() {
	case acquisition.ReleaseProtocolTorrent:
		return release.InfoHash() != "" || release.MagnetURL() != "" || release.DownloadURL() != ""
	case acquisition.ReleaseProtocolUsenet:
		return release.DownloadURL() != ""
	default:
		return false
	}
}

func rankRelease(request acquisition.SearchRequest, release acquisition.Release) releaseRank {
	seeders := -1
	if release.HasSeeders() {
		seeders = release.Seeders()
	}
	return releaseRank{
		intentMatch: releaseMatches(request, release.Title()),
		titlePrefix: releaseStartsWithTitle(request, release.Title()),
		yearMatch:   releaseMatchesYear(request, release.Title()),
		hasHash:     release.Protocol() == acquisition.ReleaseProtocolTorrent && release.InfoHash() != "",
		seeders:     seeders,
		size:        release.Size(),
		provider:    release.Provider(),
		sourceID:    release.SourceID(),
	}
}

func releaseLess(left releaseRank, right releaseRank) bool {
	if left.intentMatch != right.intentMatch {
		return left.intentMatch
	}
	if left.titlePrefix != right.titlePrefix {
		return left.titlePrefix
	}
	if left.yearMatch != right.yearMatch {
		return left.yearMatch
	}
	if left.hasHash != right.hasHash {
		return left.hasHash
	}
	if left.seeders != right.seeders {
		return left.seeders > right.seeders
	}
	if left.size != right.size {
		return left.size < right.size
	}
	if left.provider != right.provider {
		return left.provider < right.provider
	}
	return left.sourceID < right.sourceID
}

func releaseStartsWithTitle(request acquisition.SearchRequest, releaseTitle string) bool {
	title := normalizeComparisonText(request.Title())
	release := normalizeComparisonText(releaseTitle)
	return release == title || strings.HasPrefix(release, title+" ")
}

func releaseMatchesYear(request acquisition.SearchRequest, releaseTitle string) bool {
	if request.Episode() > 0 {
		return true
	}
	release := " " + normalizeComparisonText(releaseTitle) + " "
	year := fmt.Sprintf(" %d ", request.Year())
	return strings.Contains(release, year)
}

func releaseMatches(request acquisition.SearchRequest, releaseTitle string) bool {
	needle := request.Title()
	if request.Episode() > 0 {
		needle = fmt.Sprintf("%s S%02dE%02d", request.Title(), request.Season(), request.Episode())
	}
	haystack := " " + normalizeComparisonText(releaseTitle) + " "
	completeNeedle := " " + normalizeComparisonText(needle) + " "
	return strings.Contains(haystack, completeNeedle)
}

func normalizeComparisonText(value string) string {
	words := strings.Fields(strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			return unicode.ToLower(character)
		}
		return ' '
	}, value))
	return strings.Join(words, " ")
}

func releaseIdentity(release acquisition.Release) string {
	if release.InfoHash() != "" {
		return string(release.Protocol()) + "|hash|" + strings.ToLower(release.InfoHash())
	}
	return string(release.Protocol()) + "|source|" + release.Provider() + "|" + release.SourceID()
}
