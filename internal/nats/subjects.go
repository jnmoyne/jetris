package nats

import "jetricks/internal/config"

// Re-export config subject builders for convenience.
var (
	GameStream                  = config.GameStream
	GameSubjectFilter           = config.GameSubjectFilter
	CoopCellSubject              = config.CoopCellSubject
	CoopCellSubjectFilter        = config.CoopCellSubjectFilter
	CompetitiveCellSubject       = config.CompetitiveCellSubject
	CompetitiveCellSubjectFilter = config.CompetitiveCellSubjectFilter
	MetaSubject                 = config.MetaSubject
	RosterSubject               = config.RosterSubject
	EventsSubject               = config.EventsSubject
	CountdownSubject            = config.CountdownSubject
	ChatSubject                 = config.ChatSubject
	LobbyPlayerKey              = config.LobbyPlayerKey
	LobbyGameKey                = config.LobbyGameKey
)
