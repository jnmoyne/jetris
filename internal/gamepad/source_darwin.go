//go:build darwin && cgo

package gamepad

// The macOS backend: Apple's GameController framework — Xbox, PlayStation
// and Switch pads over Bluetooth or USB, and MFi ones, on macOS 11 and
// later. Everything the framework calls us on happens on queues of its
// choosing: the connect/disconnect notifications on the main queue, which
// Gio's NSApplication run loop pumps, and the value-changed handler on the
// serial queue set here. So the framework's objects are only ever touched
// there, and Go sees nothing of them: the handler writes one plain struct
// under a lock, and the poller copies it out every pollInterval
// (jetris_gamepad_read) into the digitizer. The framework does not promise
// its elements can be read off its queue; a struct under a lock needs no
// promise.
//
// One pad drives: the first one attached. Another plugged in beside it is
// ignored until the first goes, when the next one takes over.
//
// The Objective-C lives in this preamble rather than a .m file of its own:
// cmd/go links a package that has one with its own -lobjc, Gio's app
// package already brings one, and two make ld warn on every build.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -Wno-deprecated-declarations
#cgo LDFLAGS: -framework GameController -framework Foundation

#import <Foundation/Foundation.h>
#import <GameController/GameController.h>
#import <os/lock.h>


typedef struct {
	uint32_t buttons; // gamepad.Button bits (gamepad.go)
	float lx, ly;     // left stick, -1..1, ly +1 down
	float lt, rt;     // triggers, 0..1
} jetris_gamepad_state;

// The bit for each gamepad.Button, in gamepad.go's order.
enum {
	kDPadUp = 1u << 0, kDPadDown = 1u << 1, kDPadLeft = 1u << 2, kDPadRight = 1u << 3,
	kSouth = 1u << 4, kEast = 1u << 5, kWest = 1u << 6, kNorth = 1u << 7,
	kLB = 1u << 8, kRB = 1u << 9, kLT = 1u << 10, kRT = 1u << 11,
	kBack = 1u << 12, kStart = 1u << 13, kLStick = 1u << 14, kRStick = 1u << 15,
};

static os_unfair_lock jetrisLock = OS_UNFAIR_LOCK_INIT;
static jetris_gamepad_state jetrisState;
static int jetrisAttached;               // a pad is active
static GCController *jetrisActive;       // the pad that drives (main queue only)
static id jetrisConnectObs, jetrisDisconnectObs;
static dispatch_queue_t jetrisQueue;     // the value-changed handler's queue
static int jetrisStarted;

static void jetrisSnapshot(GCExtendedGamepad *gp) {
	jetris_gamepad_state s = {0};
	if (gp.dpad.up.pressed) s.buttons |= kDPadUp;
	if (gp.dpad.down.pressed) s.buttons |= kDPadDown;
	if (gp.dpad.left.pressed) s.buttons |= kDPadLeft;
	if (gp.dpad.right.pressed) s.buttons |= kDPadRight;
	if (gp.buttonA.pressed) s.buttons |= kSouth;
	if (gp.buttonB.pressed) s.buttons |= kEast;
	if (gp.buttonX.pressed) s.buttons |= kWest;
	if (gp.buttonY.pressed) s.buttons |= kNorth;
	if (gp.leftShoulder.pressed) s.buttons |= kLB;
	if (gp.rightShoulder.pressed) s.buttons |= kRB;
	if (gp.buttonOptions != nil && gp.buttonOptions.pressed) s.buttons |= kBack;
	if (gp.buttonMenu.pressed) s.buttons |= kStart;
	if (gp.leftThumbstickButton != nil && gp.leftThumbstickButton.pressed) s.buttons |= kLStick;
	if (gp.rightThumbstickButton != nil && gp.rightThumbstickButton.pressed) s.buttons |= kRStick;
	s.lx = gp.leftThumbstick.xAxis.value;
	s.ly = -gp.leftThumbstick.yAxis.value; // the framework's +1 is up; ours is down
	s.lt = gp.leftTrigger.value;
	s.rt = gp.rightTrigger.value;
	os_unfair_lock_lock(&jetrisLock);
	jetrisState = s;
	os_unfair_lock_unlock(&jetrisLock);
}

// jetrisAttach makes c the driving pad (main queue).
static void jetrisAttach(GCController *c) {
	GCExtendedGamepad *gp = c.extendedGamepad;
	if (gp == nil) {
		return; // a pad without the extended profile: not one we can drive with
	}
	jetrisActive = c;
	c.handlerQueue = jetrisQueue;
	gp.valueChangedHandler = ^(GCExtendedGamepad *g, GCControllerElement *el) {
		jetrisSnapshot(g);
	};
	jetrisSnapshot(gp);
	os_unfair_lock_lock(&jetrisLock);
	jetrisAttached = 1;
	os_unfair_lock_unlock(&jetrisLock);
}

// jetrisDetach lets the driving pad go and picks the next attached one, if
// any (main queue).
static void jetrisDetach(void) {
	if (jetrisActive != nil) {
		jetrisActive.extendedGamepad.valueChangedHandler = nil;
		jetrisActive = nil;
	}
	os_unfair_lock_lock(&jetrisLock);
	jetrisAttached = 0;
	memset(&jetrisState, 0, sizeof jetrisState);
	os_unfair_lock_unlock(&jetrisLock);
	for (GCController *c in [GCController controllers]) {
		if (c.extendedGamepad != nil) {
			jetrisAttach(c);
			return;
		}
	}
}

void jetris_gamepad_start(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (jetrisStarted) {
			return;
		}
		jetrisStarted = 1;
		jetrisQueue = dispatch_queue_create("net.jetris.gamepad", DISPATCH_QUEUE_SERIAL);
		NSNotificationCenter *nc = [NSNotificationCenter defaultCenter];
		jetrisConnectObs = [nc addObserverForName:GCControllerDidConnectNotification
		                                   object:nil
		                                    queue:[NSOperationQueue mainQueue]
		                               usingBlock:^(NSNotification *n) {
			if (jetrisActive == nil) {
				jetrisAttach((GCController *)n.object);
			}
		}];
		jetrisDisconnectObs = [nc addObserverForName:GCControllerDidDisconnectNotification
		                                      object:nil
		                                       queue:[NSOperationQueue mainQueue]
		                                  usingBlock:^(NSNotification *n) {
			if ((GCController *)n.object == jetrisActive) {
				jetrisDetach();
			}
		}];
		// The pads attached before we listened.
		jetrisDetach();
	});
}

int jetris_gamepad_read(jetris_gamepad_state *out) {
	os_unfair_lock_lock(&jetrisLock);
	int on = jetrisAttached;
	*out = jetrisState;
	os_unfair_lock_unlock(&jetrisLock);
	return on;
}

void jetris_gamepad_stop(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (!jetrisStarted) {
			return;
		}
		jetrisStarted = 0;
		NSNotificationCenter *nc = [NSNotificationCenter defaultCenter];
		if (jetrisConnectObs != nil) {
			[nc removeObserver:jetrisConnectObs];
			jetrisConnectObs = nil;
		}
		if (jetrisDisconnectObs != nil) {
			[nc removeObserver:jetrisDisconnectObs];
			jetrisDisconnectObs = nil;
		}
		if (jetrisActive != nil) {
			jetrisActive.extendedGamepad.valueChangedHandler = nil;
			jetrisActive = nil;
		}
		os_unfair_lock_lock(&jetrisLock);
		jetrisAttached = 0;
		os_unfair_lock_unlock(&jetrisLock);
	});
}*/
import "C"

type darwinSource struct{ *poller }

// Open is the platform's Source.
func Open() Source { return darwinSource{newPoller()} }

func (s darwinSource) Start(wake func()) {
	s.setWake(wake)
	C.jetris_gamepad_start()
	s.start(pollInterval, func() (Raw, bool) {
		var st C.jetris_gamepad_state
		if C.jetris_gamepad_read(&st) == 0 {
			return Raw{}, false
		}
		return Raw{
			Buttons: uint32(st.buttons),
			LX:      float32(st.lx), LY: float32(st.ly),
			LT: float32(st.lt), RT: float32(st.rt),
		}, true
	})
}

func (s darwinSource) Stop() {
	s.poller.Stop()
	C.jetris_gamepad_stop()
}
