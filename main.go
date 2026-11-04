// Discord Rich Presence Plugin for Navidrome
//
// This plugin integrates Navidrome with Discord Rich Presence. It shows how a plugin can
// keep a real-time connection to an external service while remaining completely stateless.
//
// Capabilities: Scrobbler, SchedulerCallback, WebSocketCallback
//
// NOTE: This plugin is for demonstration purposes only. It relies on the user's Discord
// token being stored in the Navidrome configuration file, which is not secure and may be
// against Discord's terms of service. Use it at your own risk.
package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
	"github.com/navidrome/navidrome/plugins/pdk/go/websocket"
)

// Configuration keys
const (
	clientIDKey             = "clientid"
	usersKey                = "users"
	activityNameKey         = "activityname"
	activityNameTemplateKey = "activitynametemplate"
	spotifyLinksKey         = "spotifylinks"
	navidromeShareLinksKey  = "navidromesharelinks"
	caaEnabledKey           = "caaenabled"
	uguuEnabledKey          = "uguuenabled"
	clearOnPauseKey         = "clearonpause"
)

const (
	navidromeWebsiteURL = "https://www.navidrome.org"

	// navidromeLogoURL is used as fallback large image when track artwork is unavailable.
	navidromeLogoURL = "https://raw.githubusercontent.com/navidrome/website/refs/heads/master/assets/icons/logo.webp"

	// Small icons 128x128
	navidromeSmallLogoURL = "https://raw.githubusercontent.com/mmvanheusden/discord-rich-presence-plugin/refs/heads/navidrome_promo/assets/navidrome_logo.png"
	pauseIconURL          = "https://raw.githubusercontent.com/navidrome/discord-rich-presence-plugin/800bfacfb8e85c33692373b10ddbd27388f262d2/assets/pause.png"
)

// Playback states from PlaybackReportRequest.State
const (
	statePlaying = "playing"
	statePaused  = "paused"
	stateStopped = "stopped"
	stateExpired = "expired"
)

// Activity name display options
const (
	activityNameDefault = "Default"
	activityNameTrack   = "Track"
	activityNameArtist  = "Artist"
	activityNameAlbum   = "Album"
	activityNameCustom  = "Custom"
)

// userConfig represents a user-token mapping from the config
type userConfig struct {
	Username       string `json:"username"`
	Token          string `json:"token"`
	PresenceStatus string `json:"presencestatus"`
}

// discordPlugin implements the scrobbler and scheduler interfaces.
type discordPlugin struct{}

// rpc handles Discord gateway communication (via websockets).
var rpc = &discordRPC{}

// init registers the plugin capabilities
func init() {
	scrobbler.Register(&discordPlugin{})
	scheduler.Register(&discordPlugin{})
	websocket.Register(rpc)
}

// getConfig loads the plugin configuration.
func getConfig() (clientID string, users map[string]userConfig, err error) {
	clientID, ok := pdk.GetConfig(clientIDKey)
	if !ok || clientID == "" {
		pdk.Log(pdk.LogWarn, "missing ClientID in configuration")
		return "", nil, nil
	}

	// Get the users array from config
	usersJSON, ok := pdk.GetConfig(usersKey)
	if !ok || usersJSON == "" {
		pdk.Log(pdk.LogWarn, "no users configured")
		return clientID, nil, nil
	}

	// Parse the JSON array
	var userConfigs []userConfig
	if err := json.Unmarshal([]byte(usersJSON), &userConfigs); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("failed to parse users config: %v", err))
		return clientID, nil, nil
	}

	if len(userConfigs) == 0 {
		pdk.Log(pdk.LogWarn, "no users configured")
		return clientID, nil, nil
	}

	// Build the users map
	users = make(map[string]userConfig)
	for _, uc := range userConfigs {
		if uc.Username != "" && uc.Token != "" {
			users[uc.Username] = uc
		}
	}

	if len(users) == 0 {
		pdk.Log(pdk.LogWarn, "no valid users configured")
		return clientID, nil, nil
	}

	return clientID, users, nil
}

// ============================================================================
// Scrobbler Implementation
// ============================================================================

// IsAuthorized checks if a user is authorized for Discord Rich Presence.
func (p *discordPlugin) IsAuthorized(input scrobbler.IsAuthorizedRequest) (bool, error) {
	_, users, err := getConfig()
	if err != nil {
		return false, fmt.Errorf("failed to check user authorization: %w", err)
	}

	_, authorized := users[input.Username]
	pdk.Log(pdk.LogInfo, fmt.Sprintf("IsAuthorized for user %s: %v", input.Username, authorized))
	return authorized, nil
}

// NowPlaying is a no-op — playback state is handled by PlaybackReport.
func (p *discordPlugin) NowPlaying(_ scrobbler.NowPlayingRequest) error {
	return nil
}

// Scrobble handles scrobble requests (no-op for Discord).
func (p *discordPlugin) Scrobble(_ scrobbler.ScrobbleRequest) error {
	// Discord Rich Presence doesn't need scrobble events
	return nil
}

// PlaybackReport handles playback state reports from Navidrome.
func (p *discordPlugin) PlaybackReport(input scrobbler.PlaybackReportRequest) error {
	pdk.Log(pdk.LogDebug, fmt.Sprintf("PlaybackReport request: %s", formatRequest(input)))
	switch input.State {
	case statePlaying:
		return p.handlePlayingOrPaused(input)
	case statePaused:
		return p.handlePlayingOrPaused(input)
	case stateStopped, stateExpired:
		return p.handleStopped(input)
	default:
		return nil
	}
}

func formatRequest(input scrobbler.PlaybackReportRequest) string {
	return fmt.Sprintf("state=%s user=%s track=%q artist=%q pos=%dms rate=%.1f player=%s",
		input.State, input.Username, input.Track.Title, input.Track.Artist,
		input.PositionMs, input.PlaybackRate, input.PlayerName)
}

func (p *discordPlugin) handlePlayingOrPaused(input scrobbler.PlaybackReportRequest) error {
	paused := input.State == statePaused

	clientID, userConfig, err := connectUser(input.Username)
	if err != nil {
		return err
	}

	clearOnPauseEnabled, _ := pdk.GetConfig(clearOnPauseKey)
	clearOnPause := clearOnPauseEnabled == "true"

	if clearOnPause && paused {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("Clearing presence for user %s, track: %s (paused=%v, clearOnPause=%v)", input.Username, input.Track.Title, paused, clearOnPause))
		return rpc.clearActivity(input.Username)
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Setting presence for user %s, track: %s (paused=%v)", input.Username, input.Track.Title, paused))

	activityName, statusDisplayType := resolveActivityName(input.Track)

	spotifyURL, artistSearchURL := resolveSpotifyLinks(input.Track)

	rate := input.PlaybackRate
	if rate <= 0 {
		rate = 1.0
	}

	// Convert track-time position to wall-clock elapsed time
	wallElapsedMs := int64(float64(input.PositionMs) / rate)
	wallDurationMs := int64(float64(int64(input.Track.Duration)*1000) / rate)

	ts := activityTimestamps{
		Start: input.Timestamp*1000 - wallElapsedMs,
		End:   input.Timestamp*1000 - wallElapsedMs + wallDurationMs,
	}
	shareURL := resolveNavidromeShareURL(input.Username, input.Track.ID)

	assets := activityAssets{
		LargeImage: getImageURL(input.Username, input.Track),
		LargeText:  input.Track.Album,
		LargeURL:   spotifyURL,
		SmallImage: navidromeSmallLogoURL,
		SmallText:  "Served by Navidrome",
		SmallURL:   shareURL,
	}

	if paused {
		ts = activityTimestamps{Start: input.Timestamp * 1000}
		assets.SmallImage = pauseIconURL
		assets.SmallText = "Paused"
		assets.SmallURL = ""
	}

	return rpc.sendActivity(clientID, input.Username, userConfig.Token, activity{
		Application:       clientID,
		Name:              activityName,
		Type:              2,
		Details:           input.Track.Title,
		DetailsURL:        spotifyURL,
		State:             input.Track.Artist,
		StateURL:          artistSearchURL,
		StatusDisplayType: statusDisplayType,
		Timestamps:        ts,
		Assets:            assets,
	}, userConfig.PresenceStatus, ts.Start)
}

func (p *discordPlugin) handleStopped(input scrobbler.PlaybackReportRequest) error {
	pdk.Log(pdk.LogInfo, fmt.Sprintf("Clearing presence for user %s", input.Username))

	clearErr := rpc.clearActivity(input.Username)
	disconnectErr := rpc.disconnect(input.Username)

	if clearErr != nil {
		return fmt.Errorf("failed to clear activity: %w", clearErr)
	}
	if disconnectErr != nil {
		return fmt.Errorf("failed to disconnect from Discord: %w", disconnectErr)
	}
	return nil
}

func connectUser(username string) (clientID string, config userConfig, err error) {
	clientID, users, err := getConfig()
	if err != nil {
		return "", userConfig{}, fmt.Errorf("failed to get config: %w", err)
	}
	if clientID == "" {
		return "", userConfig{}, fmt.Errorf("missing ClientID in configuration")
	}

	config, authorized := users[username]
	if !authorized {
		return "", userConfig{}, fmt.Errorf("%w: user '%s' not authorized", scrobbler.ScrobblerErrorNotAuthorized, username)
	}

	if err := rpc.connect(username, config.Token); err != nil {
		return "", userConfig{}, fmt.Errorf("failed to connect to Discord: %w", err)
	}
	return clientID, config, nil
}

func resolveActivityName(track scrobbler.TrackInfo) (string, int) {
	activityNameOption, _ := pdk.GetConfig(activityNameKey)
	switch activityNameOption {
	case activityNameTrack:
		return track.Title, statusDisplayName
	case activityNameAlbum:
		return track.Album, statusDisplayName
	case activityNameArtist:
		return track.Artist, statusDisplayName
	case activityNameCustom:
		template, _ := pdk.GetConfig(activityNameTemplateKey)
		if template != "" {
			artists := track.Artist
			if len(track.Artists) > 0 {
				names := make([]string, len(track.Artists))
				for i, a := range track.Artists {
					names[i] = a.Name
				}
				artists = strings.Join(names, " • ")
			}
			r := strings.NewReplacer(
				"{track}", track.Title,
				"{artist}", track.Artist,
				"{artists}", artists,
				"{album}", track.Album,
			)
			return r.Replace(template), statusDisplayName
		}
	}
	return "Navidrome", statusDisplayDetails
}

func resolveSpotifyLinks(track scrobbler.TrackInfo) (string, string) {
	spotifyLinksOption, _ := pdk.GetConfig(spotifyLinksKey)
	if spotifyLinksOption != "true" {
		return "", ""
	}
	return resolveSpotifyURL(track), spotifySearchURL(track.Artist)
}

// ============================================================================
// Navidrome Share Links
// ============================================================================

// navidromeShareCacheTTL keeps a generated share link around for a month so
// repeated playback reports for the same track don't create duplicate shares.
const navidromeShareCacheTTL int64 = 30 * 24 * 60 * 60

// resolveNavidromeShareURL returns the URL for the small "Served by Navidrome"
// button. When share links are disabled, it falls back to the Navidrome website.
func resolveNavidromeShareURL(username, trackID string) string {
	shareLinksOption, _ := pdk.GetConfig(navidromeShareLinksKey)
	if shareLinksOption != "true" {
		return navidromeWebsiteURL
	}

	shareURL, err := getNavidromeShareURL(username, trackID)
	if err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("Failed to generate Navidrome share link for track %s: %v", trackID, err))
		return navidromeWebsiteURL
	}
	return shareURL
}

// subsonicShareResponse mirrors the subset of the Subsonic createShare response
// needed to retrieve the generated share URL.
type subsonicShareResponse struct {
	SubsonicResponse struct {
		Status string         `json:"status"`
		Error  *subsonicError `json:"error,omitempty"`
		Shares *struct {
			Share []struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"share"`
		} `json:"shares,omitempty"`
	} `json:"subsonic-response"`
}

type subsonicError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// getNavidromeShareURL returns a cached share link for the track, creating one
// through the Subsonic API on a cache miss.
func getNavidromeShareURL(username, trackID string) (string, error) {
	if username == "" || trackID == "" {
		return "", fmt.Errorf("username and track ID are required to create a Navidrome share link")
	}

	cacheKey := "navidrome.share." + hashKey(username+"\x00"+trackID)
	if cached, exists, err := host.CacheGetString(cacheKey); err == nil && exists && cached != "" {
		return cached, nil
	}

	shareURL, err := generateNavidromeShareLink(username, trackID)
	if err != nil {
		return "", err
	}

	_ = host.CacheSetString(cacheKey, shareURL, navidromeShareCacheTTL)
	return shareURL, nil
}

// generateNavidromeShareLink creates a public share for the given track and
// returns the URL Navidrome generated for it. The share is created on behalf of
// username, which must be configured for this plugin and allowed to create shares.
func generateNavidromeShareLink(username, trackID string) (string, error) {
	if username == "" {
		return "", fmt.Errorf("username is required to create a Navidrome share link")
	}
	if trackID == "" {
		return "", fmt.Errorf("track ID is required to create a Navidrome share link")
	}

	params := url.Values{}
	params.Set("u", username)
	params.Set("id", trackID)

	responseJSON, err := host.SubsonicAPICall("createShare?" + params.Encode())
	if err != nil {
		return "", fmt.Errorf("failed to create Navidrome share: %w", err)
	}

	var response subsonicShareResponse
	if err := json.Unmarshal([]byte(responseJSON), &response); err != nil {
		return "", fmt.Errorf("failed to parse Navidrome share response: %w", err)
	}

	sub := response.SubsonicResponse
	if sub.Status != "ok" {
		if sub.Error != nil {
			return "", fmt.Errorf("failed to create Navidrome share: %s (code %d)", sub.Error.Message, sub.Error.Code)
		}
		return "", fmt.Errorf("failed to create Navidrome share: unexpected status %q", sub.Status)
	}
	if sub.Shares == nil || len(sub.Shares.Share) == 0 {
		return "", fmt.Errorf("failed to create Navidrome share: no share returned")
	}
	if sub.Shares.Share[0].URL == "" {
		return "", fmt.Errorf("failed to create Navidrome share: empty share URL")
	}

	return sub.Shares.Share[0].URL, nil
}

// ============================================================================
// Scheduler Callback Implementation
// ============================================================================

// OnCallback handles scheduler callbacks.
func (p *discordPlugin) OnCallback(input scheduler.SchedulerCallbackRequest) error {
	pdk.Log(pdk.LogDebug, fmt.Sprintf("Scheduler callback: id=%s, payload=%s, recurring=%v", input.ScheduleID, input.Payload, input.IsRecurring))

	switch input.Payload {
	case payloadHeartbeat:
		if err := rpc.handleHeartbeatCallback(input.ScheduleID); err != nil {
			return err
		}
	default:
		pdk.Log(pdk.LogWarn, fmt.Sprintf("Unknown scheduler callback payload: %s", input.Payload))
	}

	return nil
}

func main() {}
