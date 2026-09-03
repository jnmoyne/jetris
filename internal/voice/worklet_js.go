//go:build js

package voice

// jsWorkletSource is the AudioWorklet module the browser device loads: one
// module, two processors, both running on the browser's audio rendering
// thread where a background tab's timer throttling cannot reach them.
//
// The protocol with the Go side, sample counts always at the context's own
// rate (frameLen = round(320·rate/16000) samples is one 20 ms frame):
//
//	Go   → play : {t:"pcm",  pcm: Float32Array}   samples to queue, transferred
//	play → Go   : {t:"need", avail: n}            the ring holds n < target samples
//	                                               and no request is pending
//	cap  → Go   : {t:"cap",  pcm: Float32Array}   exactly frameLen samples, transferred
//
// jetris-play keeps a ring of `capacity` samples and asks for more once it
// holds fewer than `target` (both given in processorOptions, in samples);
// playback is pulled by the audio thread and an underrun plays silence, so
// a late answer costs a gap, never a crash or a drift. jetris-cap has no
// protocol in the other direction: it posts every frameLen samples it is
// given and its (untouched, silent) output exists only so the node can be
// wired to the destination, without which the browser never runs it.
const jsWorkletSource = `
class JetrisCap extends AudioWorkletProcessor {
	constructor(options) {
		super();
		const o = (options && options.processorOptions) || {};
		this.frameLen = o.frameLen || 128;
		this.buf = new Float32Array(this.frameLen);
		this.pos = 0;
	}
	process(inputs) {
		const ch = inputs[0] && inputs[0][0];
		if (!ch) {
			return true;
		}
		let i = 0;
		while (i < ch.length) {
			const n = Math.min(ch.length - i, this.frameLen - this.pos);
			this.buf.set(ch.subarray(i, i + n), this.pos);
			this.pos += n;
			i += n;
			if (this.pos === this.frameLen) {
				this.port.postMessage({t: "cap", pcm: this.buf}, [this.buf.buffer]);
				this.buf = new Float32Array(this.frameLen);
				this.pos = 0;
			}
		}
		return true;
	}
}

class JetrisPlay extends AudioWorkletProcessor {
	constructor(options) {
		super();
		const o = (options && options.processorOptions) || {};
		this.capacity = o.capacity || 16 * 128;
		this.target = o.target || 4 * 128;
		this.ring = new Float32Array(this.capacity);
		this.head = 0;
		this.avail = 0;
		this.pending = false;
		this.port.onmessage = (e) => {
			const m = e.data;
			if (!m || m.t !== "pcm" || !m.pcm) {
				return;
			}
			this.pending = false;
			this.push(m.pcm);
		};
	}
	push(pcm) {
		let n = Math.min(pcm.length, this.capacity - this.avail);
		let w = (this.head + this.avail) % this.capacity;
		for (let i = 0; i < n; i++) {
			this.ring[w] = pcm[i];
			w++;
			if (w === this.capacity) {
				w = 0;
			}
		}
		this.avail += n;
	}
	process(inputs, outputs) {
		const out = outputs[0] && outputs[0][0];
		if (out) {
			const take = Math.min(out.length, this.avail);
			let r = this.head;
			for (let i = 0; i < take; i++) {
				out[i] = this.ring[r];
				r++;
				if (r === this.capacity) {
					r = 0;
				}
			}
			this.head = r;
			this.avail -= take;
		}
		if (this.avail < this.target && !this.pending) {
			this.pending = true;
			this.port.postMessage({t: "need", avail: this.avail});
		}
		return true;
	}
}

registerProcessor("jetris-cap", JetrisCap);
registerProcessor("jetris-play", JetrisPlay);
`
