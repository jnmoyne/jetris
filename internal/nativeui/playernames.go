package nativeui

import (
	"fmt"
	"math/rand"
)

// The prefixes a dealt name wears, marking it as one nobody chose:
// anonymousPrefix for a player who hit Play with the field blank,
// watcherPrefix for whoever opened a replay link (no name is asked for on
// the way to a replay).
const (
	anonymousPrefix = "Anonymous_"
	watcherPrefix   = "Watcher_"
)

// playerNames are the handles a dealt name is made of: a prefix, one of
// these and a two-digit number (Anonymous_TSpinGod42), so a dealt name reads
// as one at a glance and two players dealt the same handle rarely collide.
// Every entry must pass config.ValidatePlayerName dressed that way
// (TestPlayerNamesValid). The browser's join page (web/join.html) carries
// the same list.
var playerNames = []string{
	"TSpinGod",
	"PerfectClear",
	"BackToBack",
	"TetrisReady",
	"MatrixMaster",
	"DPC_Enthusiast",
	"Maxout",
	"HyperTapper",
	"RollingPro",
	"Sub40Sprint",
	"AllClearPro",
	"MatrixRunner",
	"GravityDefier",
	"RenMaster",
	"OpenerGod",
	"SprintKing",
	"KPP_Elite",
	"TetraChamp",
	"LinePiecePls",
	"BlockParty",
	"HolyMolyTSpin",
	"GarbageSender",
	"MisdropMistake",
	"BrickLayer",
	"WellProtected",
	"HoldMyI_Piece",
	"WhereIsTheI_Piece",
	"GarbageCollector",
	"TSpinToWin",
	"BlockHead",
	"TetriSaurus",
	"OopsWrongRotate",
	"AccidentalCombo",
	"PanickedStacking",
	"GhostPiece",
	"Tetromino",
	"Z_Block",
	"Skyline",
	"Das_Charge",
	"ComboBreaker",
	"Gravity",
	"FortyLines",
	"ZeroGravity",
	"GridRunner",
	"O_Piece",
	"L_Block",
	"S_Block",
	"CyanI_Piece",
	"OrangeRicky",
	"BlueRhino",
	"ClevelandZ",
	"RhodeIslandS",
	"Teewee",
	"HeroPiece",
}

// dealName deals a name: the prefix, a random handle from playerNames and a
// random two-digit number (00–99), e.g. Anonymous_TSpinGod07. Nothing
// downstream depends on the draw, so plain math/rand is fine (the
// deterministic-RNG rules apply to the engine, not the UI).
func dealName(prefix string) string {
	return fmt.Sprintf("%s%s%02d", prefix, playerNames[rand.Intn(len(playerNames))], rand.Intn(100))
}
