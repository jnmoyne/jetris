package nats

import "jetris/internal/config"

// Re-export config subject builders for convenience.
var (
	GameStream                   = config.GameStream
	GameSubjectFilter            = config.GameSubjectFilter
	CoopCellSubject              = config.CoopCellSubject
	CoopCellSubjectFilter        = config.CoopCellSubjectFilter
	CompetitiveCellSubject       = config.CompetitiveCellSubject
	CompetitiveCellSubjectFilter = config.CompetitiveCellSubjectFilter
	TeamCellSubject              = config.TeamCellSubject
	TeamCellSubjectFilter        = config.TeamCellSubjectFilter
	MetaSubject                  = config.MetaSubject
	RosterSubject                = config.RosterSubject
	EventsSubject                = config.EventsSubject
	CountdownSubject             = config.CountdownSubject
	GameChatSubject              = config.GameChatSubject
	LobbyPlayerKey               = config.LobbyPlayerKey
	LobbyGameKey                 = config.LobbyGameKey
)
