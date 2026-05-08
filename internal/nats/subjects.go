package nats

import "jetricks/internal/config"

// Re-export config subject builders for convenience.
var (
	GameStream              = config.GameStream
	GameSubjectFilter       = config.GameSubjectFilter
	RowSubject              = config.RowSubject
	MetaSubject             = config.MetaSubject
	RosterSubject           = config.RosterSubject
	EventsSubject           = config.EventsSubject
	CountdownSubject        = config.CountdownSubject
	ChatSubject             = config.ChatSubject
	CoopScoreSubject        = config.CoopScoreSubject
	CompetitiveScoreSubject = config.CompetitiveScoreSubject
	PlayerStateSubject      = config.PlayerStateSubject
	LobbyPlayerKey          = config.LobbyPlayerKey
	LobbyGameKey            = config.LobbyGameKey
)
