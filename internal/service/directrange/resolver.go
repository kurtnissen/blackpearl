// Package directrange resolves and prepares exact provider-backed media files.
package directrange

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	acquisitionservice "github.com/blackpearl-media/blackpearl/internal/service/acquisition"
)

// CandidateGateway searches public releases and resolves their exact files.
type CandidateGateway interface {
	Search(ctx context.Context, request acquisition.SearchRequest) ([]acquisition.Release, error)
	ListRangeCandidates(ctx context.Context, release acquisition.Release) ([]acquisition.RangeCandidate, error)
}

// Resolver chooses at most one playable exact file per eligible release.
type Resolver struct {
	gateway CandidateGateway
}

// NewResolver validates the exact-file discovery dependency.
func NewResolver(gateway CandidateGateway) (*Resolver, error) {
	if gateway == nil {
		return nil, errors.New("direct range candidate gateway is required")
	}
	return &Resolver{gateway: gateway}, nil
}

// Resolve returns a bounded stable list of exact range-readable candidates.
func (r *Resolver) Resolve(ctx context.Context, request acquisition.SearchRequest) ([]acquisition.RangeCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("resolve direct range media: %w", err)
	}
	releases, err := r.gateway.Search(ctx, request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("search direct range media: %w", ctxErr)
		}
		return nil, errors.New("search direct range media is unavailable")
	}
	result := make([]acquisition.RangeCandidate, 0, min(len(releases), acquisition.MaximumJobCandidates))
	seen := make(map[string]struct{})
	eligibleItems := 0
	failedItems := 0
	for _, release := range releases {
		if !releaseEligible(request, release.Title()) {
			continue
		}
		eligibleItems++
		candidates, listErr := r.gateway.ListRangeCandidates(ctx, release)
		if listErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("list direct range media: %w", ctxErr)
			}
			failedItems++
			continue
		}
		media := make([]domain.MediaCandidate, 0, len(candidates))
		byBacking := make(map[string]acquisition.RangeCandidate, len(candidates))
		for _, candidate := range candidates {
			if hasAuxiliaryMarker(request.Title(), candidate.Media().Name) {
				continue
			}
			item := candidate.Media()
			key := item.Backing().Provider + "\x00" + item.ObjectID
			media = append(media, item)
			byBacking[key] = candidate
		}
		selected, selectErr := acquisitionservice.SelectCandidate(request, media)
		if selectErr != nil {
			continue
		}
		key := selected.Backing().Provider + "\x00" + selected.ObjectID
		candidate, ok := byBacking[key]
		if !ok {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate)
		if len(result) == acquisition.MaximumJobCandidates {
			break
		}
	}
	if eligibleItems > 0 && failedItems == eligibleItems {
		return nil, errors.New("list direct range media is unavailable")
	}
	return result, nil
}

func releaseEligible(request acquisition.SearchRequest, releaseTitle string) bool {
	release := " " + normalizeText(releaseTitle) + " "
	title := normalizeText(request.Title())
	if title == "" || hasAuxiliaryMarker(request.Title(), releaseTitle) {
		return false
	}
	if request.MediaType() == domain.MediaTypeEpisode {
		needle := fmt.Sprintf(" %s s%02de%02d ", title, request.Season(), request.Episode())
		return strings.Contains(release, needle)
	}
	if release != " "+title+" " && !strings.HasPrefix(release, " "+title+" ") {
		return false
	}
	return strings.Contains(release, fmt.Sprintf(" %d ", request.Year()))
}

func hasAuxiliaryMarker(requestTitle string, candidateTitle string) bool {
	request := " " + normalizeText(requestTitle) + " "
	candidate := " " + normalizeText(candidateTitle) + " "
	for _, marker := range [...]string{"featurette", "preview", "sample", "teaser", "trailer"} {
		needle := " " + marker + " "
		if strings.Count(candidate, needle) > strings.Count(request, needle) {
			return true
		}
	}
	return false
}

func normalizeText(value string) string {
	words := strings.Fields(strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			return unicode.ToLower(character)
		}
		return ' '
	}, value))
	return strings.Join(words, " ")
}
