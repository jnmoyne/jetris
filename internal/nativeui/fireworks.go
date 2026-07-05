package nativeui

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"math/rand"
	"sync"
	"time"

	"gioui.org/op"
	"gioui.org/op/clip"
)

// Victory fireworks, shown over the whole game screen when the local player
// (competitive) or their team (teams) wins. A show is generated once, when
// UpdateGameOver{Won: true} arrives, and every frame after that is a pure
// function of gtx.Now — the countdown/CAS-flash idiom: layoutGame keeps
// calling invalidate() while the show is active, and no per-frame state is
// mutated. Rockets launch from the bottom edge, rise to a random apex, and
// every one explodes into a small NATS "N" logo built from particles sampled
// off the embedded nats-icon.png: the logo pops in, holds, then splits into
// its small squares and flies apart in all directions like a bursting shell.
// The show loops — the overlay draws at elapsed time modulo the cycle — until
// the App drops it (back to lobby, or a new game starting).

const (
	fwRocketCount = 12
	fwLaunchGap   = 420 * time.Millisecond  // stagger between rocket launches
	fwLogoDur     = 2400 * time.Millisecond // life of one logo burst: pop in, hold, scatter
	fwLogoGrid    = 22                      // sampling grid over nats-icon.png (one particle per opaque sample)
)

// fwWhite is the warm white of the rising rocket streaks.
var fwWhite = colorN{R: 0xff, G: 0xf2, B: 0xd0, A: 0xff}

// fwRocket is one launch: positions are window fractions so the show scales
// with the window; times are offsets from the show start.
type fwRocket struct {
	fx     float64       // launch/apex x, fraction of width
	apexFy float64       // apex y, fraction of height
	launch time.Duration // when the rocket leaves the bottom edge
	rise   time.Duration // bottom edge → apex
}

type fireworksShow struct {
	start   time.Time
	cycle   time.Duration // one full pass (launch+rise+burst of the last-finishing rocket); the show replays it until dropped
	rockets []fwRocket
}

// newFireworksShow rolls a complete show. The seed only varies the look —
// nothing downstream depends on it, so plain math/rand is fine here (the
// deterministic-RNG rules apply to the engine, not the UI).
func newFireworksShow(now time.Time) *fireworksShow {
	rng := rand.New(rand.NewSource(now.UnixNano()))
	show := &fireworksShow{start: now}
	for i := 0; i < fwRocketCount; i++ {
		r := fwRocket{
			fx:     0.08 + 0.84*rng.Float64(),
			apexFy: 0.10 + 0.35*rng.Float64(),
			launch: time.Duration(i)*fwLaunchGap + time.Duration(rng.Int63n(int64(200*time.Millisecond))),
			rise:   550*time.Millisecond + time.Duration(rng.Int63n(int64(350*time.Millisecond))),
		}
		if end := r.launch + r.rise + fwLogoDur; end > show.cycle {
			show.cycle = end
		}
		show.rockets = append(show.rockets, r)
	}
	return show
}

// active reports whether the show is running at now. Once started it never
// ends on its own — it loops until the App drops it (returnToLobby /
// startGameScreen set a.fireworks = nil).
func (fw *fireworksShow) active(now time.Time) bool {
	return now.Sub(fw.start) >= 0
}

// fireworksOverlay draws the show at gtx.Now over the full constraint area.
// Pure paint ops — no event.Op — so input still reaches the widgets below.
func fireworksOverlay(gtx C, fw *fireworksShow) D {
	sz := gtx.Constraints.Max
	defer clip.Rect(image.Rectangle{Max: sz}).Push(gtx.Ops).Pop()

	el := gtx.Now.Sub(fw.start)
	if el < 0 {
		return D{Size: sz}
	}
	el %= fw.cycle // the show loops: replay the same choreography each cycle
	w, h := float64(sz.X), float64(sz.Y)
	m := math.Min(w, h)
	for i := range fw.rockets {
		r := &fw.rockets[i]
		t := el - r.launch
		if t < 0 || t >= r.rise+fwLogoDur {
			continue
		}
		x := r.fx * w
		apexY := r.apexFy * h
		if t < r.rise {
			drawRocketStreak(gtx.Ops, x, apexY, h, m, float64(t)/float64(r.rise))
		} else {
			// 0..1 through the explosion
			drawLogoBurst(gtx.Ops, x, apexY, m, float64(t-r.rise)/float64(fwLogoDur))
		}
	}
	return D{Size: sz}
}

// drawRocketStreak draws the ascending rocket at progress p (0 at the bottom
// edge, 1 at the apex): a bright head with a short fading tail.
func drawRocketStreak(ops *op.Ops, x, apexY, h, m, p float64) {
	y := h - easeOutCubic(p)*(h-apexY)
	wpx := int(math.Max(2, 0.004*m))
	head := int(math.Max(6, 0.014*m))
	px := int(math.Round(x))
	fillRect(ops, image.Rect(px-wpx/2, int(y), px-wpx/2+wpx, int(y)+head), fwWhite)
	for i := 1; i <= 2; i++ {
		ty := int(y) + head + i*head
		fillRect(ops, image.Rect(px-wpx/2, ty, px-wpx/2+wpx, ty+head/2), withAlpha(fwWhite, 0.5/float64(i)))
	}
}

// fwScatterStart is where in a burst's life (0..1) the assembled logo blows
// apart: pop-in runs over the first 25%, the intact logo holds until here,
// then every block flies out radially while fading.
const fwScatterStart = 0.5

// drawLogoBurst draws one NATS-logo explosion at progress te. Three phases:
// the sampled logo particles pop out to a small logo (easeOutBack over the
// first 25%), the logo holds while drifting down slightly, then — like a
// shell bursting — it splits into its small squares and they fly apart: each
// block shrinks inside its grid cell (seams appear, so the break-up is
// visible the moment the scatter starts) and flings outward along its own
// radial direction with per-block speed jitter, drooping under gravity. The
// split reads through size, not alpha — the debris keeps shrinking as it
// flies instead of dimming in place, with a short fade only at the very tail.
func drawLogoBurst(ops *op.Ops, cx, cy, m, te float64) {
	pts := fwLogoPoints()
	if len(pts) == 0 {
		return
	}
	ts := clampF((te-fwScatterStart)/(1-fwScatterStart), 0, 1) // progress through the scatter phase
	alpha := clampF((1-ts)/0.25, 0, 1)                         // solid while flying, gone over the last quarter
	if alpha <= 0 {
		return
	}
	side := 0.15 * m * easeOutBack(clampF(te/0.25, 0, 1))
	droop := 0.06*m*te*te + 0.12*m*ts*ts // gentle drift while intact, real gravity on the flying blocks
	fling := 0.35 * m * easeOutCubic(ts)
	dot := int(math.Max(2, 0.15*m/fwLogoGrid))
	blk := int(math.Max(1, float64(dot)*(1-0.75*ts))) // block edge shrinks from a full cell to a spark
	off := (dot - blk) / 2                            // keep each shrinking block centered in its cell
	for _, p := range pts {
		px := int(math.Round(cx+p.dx*side+p.sx*fling)) + off
		py := int(math.Round(cy+p.dy*side+p.sy*fling+droop)) + off
		fillRect(ops, image.Rect(px, py, px+blk, py+blk), withAlpha(p.col, alpha))
	}
}

// easeOutCubic eases 0→1 fast-then-slow, used for rocket rise and spark spread.
func easeOutCubic(t float64) float64 {
	u := 1 - clampF(t, 0, 1)
	return 1 - u*u*u
}

// fwLogoPt is one particle of the NATS-logo burst: an offset in [-0.5, 0.5]²
// around the explosion center, the logo pixel's color, and the scatter
// velocity (radial from the logo center, with per-block speed jitter) used
// when the assembled logo blows apart.
type fwLogoPt struct {
	dx, dy float64
	sx, sy float64
	col    colorN
}

var (
	fwLogoOnce sync.Once
	fwLogoPts  []fwLogoPt
)

// fwLogoPoints samples the embedded nats-icon.png once on a fwLogoGrid² grid,
// keeping a particle (with the pixel's color) for every mostly-opaque sample —
// so the burst reproduces the real logo, white "N" and all four quadrants.
// Each particle also gets its scatter velocity: outward through its own
// position (so the explosion radiates from the logo center), a random
// direction for blocks sitting at the very center, and 0.6–1.4× speed jitter
// so the blow-apart looks like debris rather than a scaled-up logo. The
// jitter comes from a fixed-seed RNG — the table is deterministic, and shows
// still differ from each other via rocket timing/placement.
func fwLogoPoints() []fwLogoPt {
	fwLogoOnce.Do(func() {
		img, _, err := image.Decode(bytes.NewReader(natsIconPNG))
		if err != nil {
			return
		}
		rng := rand.New(rand.NewSource(42))
		b := img.Bounds()
		for gy := 0; gy < fwLogoGrid; gy++ {
			for gx := 0; gx < fwLogoGrid; gx++ {
				px := b.Min.X + (2*gx+1)*b.Dx()/(2*fwLogoGrid)
				py := b.Min.Y + (2*gy+1)*b.Dy()/(2*fwLogoGrid)
				c := color.NRGBAModel.Convert(img.At(px, py)).(color.NRGBA)
				if c.A < 128 {
					continue
				}
				c.A = 0xff
				dx := float64(gx)/(fwLogoGrid-1) - 0.5
				dy := float64(gy)/(fwLogoGrid-1) - 0.5
				ang := math.Atan2(dy, dx)
				if math.Hypot(dx, dy) < 0.05 {
					ang = 2 * math.Pi * rng.Float64()
				}
				speed := 0.6 + 0.8*rng.Float64()
				fwLogoPts = append(fwLogoPts, fwLogoPt{
					dx: dx, dy: dy,
					sx: speed * math.Cos(ang), sy: speed * math.Sin(ang),
					col: c,
				})
			}
		}
	})
	return fwLogoPts
}
