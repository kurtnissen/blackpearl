package acquisition

import (
	"crypto/sha1" // #nosec G505 -- BitTorrent v1 defines its content identity with SHA-1.
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	// MaximumTorrentFileBytes bounds transient Prowlarr and TorBox torrent
	// material. The payload is never persisted by BlackPearl.
	MaximumTorrentFileBytes = 4 << 20
	maximumBencodeDepth     = 64
)

// TorrentInputKind identifies the transient locator sent to a preparation
// provider.
type TorrentInputKind string

const (
	TorrentInputMagnet TorrentInputKind = "magnet"
	TorrentInputFile   TorrentInputKind = "file"
)

// TorrentInput is a validated transient torrent locator. It is deliberately
// unsuitable for JSON persistence and returns defensive copies of file bytes.
type TorrentInput struct {
	kind     TorrentInputKind
	infoHash string
	magnet   string
	file     []byte
}

// NewMagnetTorrentInput validates a magnet against the selected info hash.
func NewMagnetTorrentInput(expectedInfoHash string, magnet string) (TorrentInput, error) {
	expected, err := normalizeInfoHash(expectedInfoHash)
	if err != nil || expected == "" {
		return TorrentInput{}, errors.New("torrent input requires a valid expected info hash")
	}
	if err := validateMagnetURL(magnet); err != nil {
		return TorrentInput{}, fmt.Errorf("validate torrent input magnet: %w", err)
	}
	parsed, err := url.Parse(magnet)
	if err != nil {
		return TorrentInput{}, errors.New("parse torrent input magnet")
	}
	matched := false
	for _, topic := range parsed.Query()["xt"] {
		const prefix = "urn:btih:"
		if len(topic) <= len(prefix) || !strings.EqualFold(topic[:len(prefix)], prefix) {
			continue
		}
		hash, hashErr := normalizeInfoHash(topic[len(prefix):])
		if hashErr == nil && strings.EqualFold(hash, expected) {
			matched = true
			break
		}
	}
	if !matched {
		return TorrentInput{}, errors.New("torrent input magnet does not match selected info hash")
	}
	return TorrentInput{kind: TorrentInputMagnet, infoHash: expected, magnet: magnet}, nil
}

// NewTorrentFileInput verifies the raw bencoded info dictionary against the
// selected BitTorrent v1 info hash.
func NewTorrentFileInput(expectedInfoHash string, payload []byte) (TorrentInput, error) {
	expected, err := normalizeInfoHash(expectedInfoHash)
	if err != nil || expected == "" {
		return TorrentInput{}, errors.New("torrent input requires a valid expected info hash")
	}
	if len(payload) == 0 {
		return TorrentInput{}, errors.New("torrent file is required")
	}
	if len(payload) > MaximumTorrentFileBytes {
		return TorrentInput{}, fmt.Errorf("torrent file exceeds %d bytes", MaximumTorrentFileBytes)
	}
	actual, err := rawTorrentInfoHash(payload)
	if err != nil {
		return TorrentInput{}, fmt.Errorf("validate torrent file: %w", err)
	}
	if !strings.EqualFold(actual, expected) {
		return TorrentInput{}, errors.New("torrent file info hash does not match selected release")
	}
	return TorrentInput{kind: TorrentInputFile, infoHash: expected, file: append([]byte(nil), payload...)}, nil
}

// Kind returns whether this input contains a magnet or torrent file.
func (i TorrentInput) Kind() TorrentInputKind { return i.kind }

// InfoHash returns the validated stable content fingerprint.
func (i TorrentInput) InfoHash() string { return i.infoHash }

// Magnet returns the validated magnet or an empty string.
func (i TorrentInput) Magnet() string { return i.magnet }

// File returns an independent copy of the bounded torrent payload.
func (i TorrentInput) File() []byte { return append([]byte(nil), i.file...) }

func rawTorrentInfoHash(payload []byte) (string, error) {
	parser := bencodeParser{payload: payload}
	if !parser.consume('d') {
		return "", errors.New("torrent payload must be a bencoded dictionary")
	}
	var info []byte
	for !parser.atEnd() && parser.peek() != 'e' {
		key, err := parser.readBytes()
		if err != nil {
			return "", fmt.Errorf("read torrent dictionary key: %w", err)
		}
		start := parser.offset
		if err := parser.skipValue(1); err != nil {
			return "", fmt.Errorf("read torrent dictionary value: %w", err)
		}
		if string(key) == "info" {
			if info != nil {
				return "", errors.New("torrent payload contains duplicate info dictionary")
			}
			if start >= len(payload) || payload[start] != 'd' {
				return "", errors.New("torrent info value must be a dictionary")
			}
			info = payload[start:parser.offset]
		}
	}
	if !parser.consume('e') || !parser.atEnd() {
		return "", errors.New("torrent payload has an unterminated dictionary or trailing data")
	}
	if len(info) == 0 {
		return "", errors.New("torrent payload is missing its info dictionary")
	}
	sum := sha1.Sum(info) // #nosec G401 -- mandated BitTorrent v1 info-hash algorithm.
	return hex.EncodeToString(sum[:]), nil
}

type bencodeParser struct {
	payload []byte
	offset  int
}

func (p *bencodeParser) atEnd() bool { return p.offset >= len(p.payload) }

func (p *bencodeParser) peek() byte {
	if p.atEnd() {
		return 0
	}
	return p.payload[p.offset]
}

func (p *bencodeParser) consume(want byte) bool {
	if p.peek() != want {
		return false
	}
	p.offset++
	return true
}

func (p *bencodeParser) readBytes() ([]byte, error) {
	start := p.offset
	for !p.atEnd() && p.peek() >= '0' && p.peek() <= '9' {
		p.offset++
	}
	if start == p.offset || !p.consume(':') {
		return nil, errors.New("bencoded byte string length is invalid")
	}
	lengthText := string(p.payload[start : p.offset-1])
	if len(lengthText) > 1 && lengthText[0] == '0' {
		return nil, errors.New("bencoded byte string length is not canonical")
	}
	length, err := strconv.ParseUint(lengthText, 10, 31)
	if err != nil || uint64(len(p.payload)-p.offset) < length {
		return nil, errors.New("bencoded byte string exceeds payload")
	}
	end := p.offset + int(length)
	value := p.payload[p.offset:end]
	p.offset = end
	return value, nil
}

func (p *bencodeParser) skipValue(depth int) error {
	if depth > maximumBencodeDepth || p.atEnd() {
		return errors.New("bencoded value exceeds depth or payload limit")
	}
	switch p.peek() {
	case 'i':
		return p.skipInteger()
	case 'l':
		p.offset++
		for !p.atEnd() && p.peek() != 'e' {
			if err := p.skipValue(depth + 1); err != nil {
				return err
			}
		}
		if !p.consume('e') {
			return errors.New("unterminated bencoded list")
		}
		return nil
	case 'd':
		p.offset++
		for !p.atEnd() && p.peek() != 'e' {
			if _, err := p.readBytes(); err != nil {
				return err
			}
			if err := p.skipValue(depth + 1); err != nil {
				return err
			}
		}
		if !p.consume('e') {
			return errors.New("unterminated bencoded dictionary")
		}
		return nil
	default:
		if p.peek() < '0' || p.peek() > '9' {
			return errors.New("unsupported bencoded value")
		}
		_, err := p.readBytes()
		return err
	}
}

func (p *bencodeParser) skipInteger() error {
	p.offset++
	start := p.offset
	if p.peek() == '-' {
		p.offset++
	}
	digits := p.offset
	for !p.atEnd() && p.peek() >= '0' && p.peek() <= '9' {
		p.offset++
	}
	if digits == p.offset || !p.consume('e') {
		return errors.New("bencoded integer is invalid")
	}
	number := string(p.payload[start : p.offset-1])
	if number == "-0" || strings.HasPrefix(number, "00") || strings.HasPrefix(number, "-0") {
		return errors.New("bencoded integer is not canonical")
	}
	return nil
}
