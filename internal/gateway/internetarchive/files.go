package internetarchive

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

const (
	// FileProviderName is the stable backing provider for exact Archive files.
	FileProviderName          = "internet-archive-file"
	maximumRangeCandidates    = 100
	maximumRangeObjectIDBytes = 512
)

type archiveMetadataEnvelope struct {
	Metadata struct {
		LicenseURL json.RawMessage `json:"licenseurl"`
	} `json:"metadata"`
	Files  []archiveFile `json:"files"`
	Server string        `json:"server"`
	Dir    string        `json:"dir"`
}

type archiveFile struct {
	Name   string          `json:"name"`
	Size   json.RawMessage `json:"size"`
	SHA1   string          `json:"sha1"`
	Format string          `json:"format"`
	Source string          `json:"source"`
}

type exactFile struct {
	name   string
	size   int64
	sha1   string
	source string
}

// ListRangeCandidates resolves a public Archive search result to licensed,
// exact, independently range-readable media files.
func (g *Gateway) ListRangeCandidates(ctx context.Context, release acquisition.Release) ([]acquisition.RangeCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list Internet Archive files: %w", err)
	}
	if release.Provider() != providerName || release.SourceID() == "" {
		return nil, errors.New("internet Archive file listing requires its validated release")
	}
	if _, err := archiveTorrentURL(g.baseURL, release.SourceID()); err != nil {
		return nil, errors.New("internet Archive file listing has an invalid item identifier")
	}
	metadata, err := g.fetchMetadata(ctx, release.SourceID())
	if err != nil {
		return nil, err
	}
	if !supportedLicense(metadata.Metadata.LicenseURL) {
		return nil, errors.New("internet Archive item does not declare a supported open license")
	}
	files := eligibleExactFiles(metadata.Files)
	result := make([]acquisition.RangeCandidate, 0, len(files))
	for _, file := range files {
		objectID, encodeErr := encodeFileObjectID(release.SourceID(), file.name)
		if encodeErr != nil {
			continue
		}
		media, mediaErr := domain.NewProviderMediaCandidate(
			domain.BackingRef{Provider: FileProviderName, ObjectID: objectID}, file.name, file.size,
		)
		if mediaErr != nil {
			continue
		}
		candidate, candidateErr := acquisition.NewRangeCandidate(media, internetArchiveTag)
		if candidateErr == nil {
			result = append(result, candidate)
		}
		if len(result) == maximumRangeCandidates {
			break
		}
	}
	return result, nil
}

func (g *Gateway) fetchMetadata(ctx context.Context, identifier string) (_ archiveMetadataEnvelope, resultErr error) {
	endpoint, err := url.JoinPath(g.baseURL.String(), "metadata", identifier)
	if err != nil {
		return archiveMetadataEnvelope{}, errors.New("construct Internet Archive metadata URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return archiveMetadataEnvelope{}, errors.New("construct Internet Archive metadata request")
	}
	response, err := g.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return archiveMetadataEnvelope{}, fmt.Errorf("request Internet Archive metadata: %w", ctxErr)
		}
		return archiveMetadataEnvelope{}, errors.New("request Internet Archive metadata")
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.New("close Internet Archive metadata response"))
		}
	}()
	if response.StatusCode != http.StatusOK {
		return archiveMetadataEnvelope{}, fmt.Errorf("internet Archive metadata returned HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumBodyBytes+1))
	if err != nil {
		return archiveMetadataEnvelope{}, errors.New("read Internet Archive metadata response")
	}
	if len(body) > maximumBodyBytes {
		return archiveMetadataEnvelope{}, errors.New("internet Archive metadata response exceeds 2 MiB")
	}
	var metadata archiveMetadataEnvelope
	if err := json.Unmarshal(body, &metadata); err != nil {
		return archiveMetadataEnvelope{}, errors.New("decode Internet Archive metadata response")
	}
	return metadata, nil
}

func supportedLicense(raw json.RawMessage) bool {
	var values []string
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		values = append(values, single)
	} else if err := json.Unmarshal(raw, &values); err != nil {
		return false
	}
	for _, value := range values {
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if host != "creativecommons.org" && host != "www.creativecommons.org" {
			continue
		}
		cleanPath := strings.ToLower(path.Clean(parsed.Path)) + "/"
		if strings.HasPrefix(cleanPath, "/licenses/") || strings.HasPrefix(cleanPath, "/publicdomain/") {
			return true
		}
	}
	return false
}

func eligibleExactFiles(files []archiveFile) []exactFile {
	result := make([]exactFile, 0, len(files))
	for _, file := range files {
		source := strings.ToLower(strings.TrimSpace(file.Source))
		if source != "original" && source != "derivative" {
			continue
		}
		size, err := parsePositiveSize(file.Size)
		if err != nil {
			continue
		}
		sha := strings.ToLower(strings.TrimSpace(file.SHA1))
		decoded, err := hex.DecodeString(sha)
		if err != nil || len(decoded) != 20 {
			continue
		}
		name := strings.TrimSpace(strings.ReplaceAll(file.Name, "\\", "/"))
		extension := strings.ToLower(path.Ext(name))
		if extension != ".mp4" && extension != ".mkv" {
			continue
		}
		result = append(result, exactFile{name: name, size: size, sha1: sha, source: source})
	}
	sort.Slice(result, func(left int, right int) bool {
		leftOriginal := result[left].source == "original"
		rightOriginal := result[right].source == "original"
		if leftOriginal != rightOriginal {
			return leftOriginal
		}
		return strings.ToLower(result[left].name) < strings.ToLower(result[right].name)
	})
	return result
}

func parsePositiveSize(raw json.RawMessage) (int64, error) {
	var size int64
	if err := json.Unmarshal(raw, &size); err == nil && size > 0 {
		return size, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, errors.New("invalid Archive file size")
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != text {
		return 0, errors.New("invalid Archive file size")
	}
	return parsed, nil
}

func encodeFileObjectID(identifier string, filename string) (string, error) {
	if _, err := archiveTorrentURL(&url.URL{Scheme: "https", Host: "archive.org"}, identifier); err != nil {
		return "", errors.New("invalid Archive file identifier")
	}
	if _, err := domain.NewProviderMediaCandidate(
		domain.BackingRef{Provider: FileProviderName, ObjectID: "validation"}, filename, 1,
	); err != nil {
		return "", errors.New("invalid Archive file name")
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(identifier)) + "~" + base64.RawURLEncoding.EncodeToString([]byte(filename))
	if len(encoded) > maximumRangeObjectIDBytes {
		return "", errors.New("Archive file identity is too long")
	}
	return encoded, nil
}

func decodeFileObjectID(objectID string) (string, string, error) {
	if len(objectID) == 0 || len(objectID) > maximumRangeObjectIDBytes || strings.Count(objectID, "~") != 1 {
		return "", "", errors.New("invalid Archive file identity")
	}
	parts := strings.SplitN(objectID, "~", 2)
	identifierBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", errors.New("invalid Archive file identity")
	}
	filenameBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", errors.New("invalid Archive file identity")
	}
	identifier, filename := string(identifierBytes), string(filenameBytes)
	reencoded, err := encodeFileObjectID(identifier, filename)
	if err != nil || reencoded != objectID {
		return "", "", errors.New("invalid Archive file identity")
	}
	return identifier, filename, nil
}

func findExactFile(files []archiveFile, filename string) (exactFile, error) {
	for _, file := range eligibleExactFiles(files) {
		if file.name == filename {
			return file, nil
		}
	}
	return exactFile{}, errors.New("Archive file is no longer available")
}

func (g *Gateway) fileDownloadURL(metadata archiveMetadataEnvelope, identifier string, filename string) (string, error) {
	if strings.EqualFold(g.baseURL.Scheme, "https") && strings.EqualFold(g.baseURL.Hostname(), "archive.org") && metadata.Server != "" && metadata.Dir != "" {
		server := strings.ToLower(strings.TrimSpace(metadata.Server))
		cleanDir := path.Clean(strings.TrimSpace(metadata.Dir))
		if !strings.HasSuffix(server, ".archive.org") || strings.Contains(server, ":") || !strings.HasPrefix(cleanDir, "/") || cleanDir != metadata.Dir {
			return "", errors.New("internet Archive metadata contains an invalid download location")
		}
		location := &url.URL{Scheme: "https", Host: server, Path: path.Join(cleanDir, filename)}
		return location.String(), nil
	}
	location, err := url.JoinPath(g.baseURL.String(), "download", identifier, filename)
	if err != nil {
		return "", errors.New("construct Internet Archive file URL")
	}
	return location, nil
}
