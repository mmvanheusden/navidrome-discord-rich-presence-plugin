package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

// hashKey returns a hex-encoded FNV-1a hash of s, for use as a cache key suffix.
func hashKey(s string) string {
	const offset64 uint64 = 14695981039346656037
	const prime64 uint64 = 1099511628211
	h := offset64
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return fmt.Sprintf("%016x", h)
}

const (
	spotifyCacheTTLHit  int64 = 30 * 24 * 60 * 60 // 30 days for resolved track IDs
	spotifyCacheTTLMiss int64 = 4 * 60 * 60       // 4 hours for misses (retry later)
)

// listenBrainzResult captures the relevant field from ListenBrainz Labs JSON responses.
// The API returns spotify_track_ids as an array of strings.
type listenBrainzResult struct {
	SpotifyTrackIDs []string `json:"spotify_track_ids"`
}

// spotifySearchURL builds a Spotify search URL from one or more terms.
// Empty terms are ignored. Returns "" if all terms are empty.
func spotifySearchURL(terms ...string) string {
	query := strings.TrimSpace(strings.Join(terms, " "))
	if query == "" {
		return ""
	}
	return "https://open.spotify.com/search/" + url.PathEscape(query)
}

// spotifyCacheKey returns a deterministic cache key for a track's Spotify URL.
func spotifyCacheKey(artist, title, album string) string {
	return "spotify.url." + hashKey(strings.ToLower(artist)+"\x00"+strings.ToLower(title)+"\x00"+strings.ToLower(album))
}

// resolveSpotifyURL resolves a direct Spotify track URL via ListenBrainz Labs,
// falling back to a search URL. Results are cached.
func resolveSpotifyURL(track scrobbler.TrackInfo) string {
	var primary string
	if len(track.Artists) > 0 {
		primary = track.Artists[0].Name
	}

	cacheKey := spotifyCacheKey(primary, track.Title, track.Album)

	if cached, exists, err := host.CacheGetString(cacheKey); err == nil && exists {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("Spotify URL cache hit for %q - %q → %s", primary, track.Title, cached))
		return cached
	}

	//pdk.Log(pdk.LogDebug, fmt.Sprintf("Resolving Spotify URL for: artist=%q title=%q album=%q mbid=%q", primary, track.Title, track.Album, track.MBZRecordingID))
	//
	//// 1. Try MBID lookup (most accurate)
	//if track.MBZRecordingID != "" {
	//	if trackID := trySpotifyFromMBID(track.MBZRecordingID); trackID != "" {
	//		directURL := "https://open.spotify.com/track/" + trackID
	//		_ = host.CacheSetString(cacheKey, directURL, spotifyCacheTTLHit)
	//		pdk.Log(pdk.LogInfo, fmt.Sprintf("Resolved Spotify via MBID for %q: %s", track.Title, directURL))
	//		return directURL
	//	}
	//	pdk.Log(pdk.LogDebug, "MBID lookup did not return a Spotify ID, trying metadata…")
	//} else {
	//	pdk.Log(pdk.LogDebug, "No MBZRecordingID available, skipping MBID lookup")
	//}
	//
	//// 2. Try metadata lookup
	//if primary != "" && track.Title != "" {
	//	if trackID := trySpotifyFromMetadata(primary, track.Title, track.Album); trackID != "" {
	//		directURL := "https://open.spotify.com/track/" + trackID
	//		_ = host.CacheSetString(cacheKey, directURL, spotifyCacheTTLHit)
	//		pdk.Log(pdk.LogInfo, fmt.Sprintf("Resolved Spotify via metadata for %q - %q: %s", primary, track.Title, directURL))
	//		return directURL
	//	}
	//}

	// M: ALWAYS RETURN SPOTIFY SEARCH URL, BECASE WE DON'T BOTHER.
	// 3. Fallback to search URL
	searchURL := spotifySearchURL(track.Title, track.Artist)
	_ = host.CacheSetString(cacheKey, searchURL, spotifyCacheTTLMiss)
	pdk.Log(pdk.LogInfo, fmt.Sprintf("Spotify resolution missed, falling back to search URL for %q - %q: %s", primary, track.Title, searchURL))
	return searchURL
}
