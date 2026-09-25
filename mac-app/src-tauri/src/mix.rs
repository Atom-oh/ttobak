//! Pure sample processing for native capture: splitting one Core Audio
//! aggregate-device input buffer list into microphone and system-audio (tap)
//! channels, mixing them to interleaved stereo, and resampling to the 16 kHz
//! mono PCM used for live captions.
//!
//! Deliberately free of FFI and not `cfg`-gated, so these rules are unit
//! tested on every platform: this module has no CI on macOS, and a mis-split
//! channel layout silently produces a garbled recording.

/// One `AudioBuffer` from the IOProc's input list: `channels` interleaved
/// f32 channels in `data` (Core Audio's canonical IOProc format).
#[derive(Clone, Copy, Debug)]
pub struct BufferView<'a> {
    pub channels: usize,
    pub data: &'a [f32],
}

/// Where each source's channels sit in the aggregate's input channel order.
/// Aggregate input streams list sub-device inputs first (in sub-device list
/// order) and taps last; `skip` covers sub-device inputs that are neither the
/// selected microphone nor the tap (e.g. a headset clock device in tap-only
/// mode), then `mic` channels, then every remaining channel is system audio.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ChannelLayout {
    pub skip: usize,
    pub mic: usize,
}

/// Result of mixing one callback.
#[derive(Debug, Default, PartialEq)]
pub struct Mixed {
    /// Interleaved stereo (L, R) frames.
    pub stereo: Vec<f32>,
    /// Peak absolute sample of the system-audio (tap) channels.
    pub system_peak: f32,
    /// Peak absolute sample of the microphone channels.
    pub mic_peak: f32,
    /// Number of system-audio channels actually found.
    pub system_channels: usize,
}

/// Microphone gain relative to system audio. Remote voices arrive at playback
/// level while a laptop/USB microphone is usually quieter; unity keeps the
/// mix honest, the soft clip below absorbs the occasional sum above 1.0.
pub const MIC_GAIN: f32 = 1.0;

/// Mix one IOProc callback. Frames = the shortest non-empty buffer's frame
/// count (the aggregate delivers equal-length buffers; a short buffer only
/// truncates this callback instead of misaligning channels). A buffer with no
/// data (a null `mData`) contributes silence rather than erasing the others.
pub fn mix(buffers: &[BufferView<'_>], layout: ChannelLayout, mic_gain: f32) -> Mixed {
    let frames = buffers
        .iter()
        .filter(|b| b.channels > 0 && !b.data.is_empty())
        .map(|b| b.data.len() / b.channels)
        .min()
        .unwrap_or(0);
    if frames == 0 {
        return Mixed::default();
    }

    // Map every global channel index to (buffer, channel within buffer).
    let mut channel_map: Vec<(usize, usize)> = Vec::new();
    for (bi, b) in buffers.iter().enumerate() {
        for ci in 0..b.channels {
            channel_map.push((bi, ci));
        }
    }
    let mic_start = layout.skip.min(channel_map.len());
    let mic_end = (layout.skip + layout.mic).min(channel_map.len());
    let mic_channels = &channel_map[mic_start..mic_end];
    let system_channels = &channel_map[mic_end..];

    let sample = |&(bi, ci): &(usize, usize), frame: usize| -> f32 {
        let b = &buffers[bi];
        b.data.get(frame * b.channels + ci).copied().unwrap_or(0.0)
    };

    let mut out = Mixed {
        stereo: Vec::with_capacity(frames * 2),
        system_channels: system_channels.len(),
        ..Mixed::default()
    };
    for frame in 0..frames {
        let (mut left, mut right) = match system_channels {
            [] => (0.0, 0.0),
            [only] => {
                let s = sample(only, frame);
                (s, s)
            }
            [l, r, ..] => (sample(l, frame), sample(r, frame)),
        };
        out.system_peak = out.system_peak.max(left.abs()).max(right.abs());

        if !mic_channels.is_empty() {
            let mono: f32 = mic_channels.iter().map(|c| sample(c, frame)).sum::<f32>()
                / mic_channels.len() as f32;
            out.mic_peak = out.mic_peak.max(mono.abs());
            left += mono * mic_gain;
            right += mono * mic_gain;
        }
        out.stereo.push(soft_clip(left));
        out.stereo.push(soft_clip(right));
    }
    out
}

/// Identity below 0.9, then a tanh knee that approaches (never exceeds) 1.0
/// (f32 rounding reaches exactly 1.0 for large inputs),
/// so summed mic + system peaks do not hard-clip into audible distortion.
pub fn soft_clip(x: f32) -> f32 {
    const KNEE: f32 = 0.9;
    let a = x.abs();
    if a <= KNEE || !a.is_finite() {
        return if a.is_finite() { x } else { 0.0 };
    }
    let shaped = KNEE + (1.0 - KNEE) * ((a - KNEE) / (1.0 - KNEE)).tanh();
    shaped.copysign(x)
}

/// Average interleaved stereo frames to mono.
pub fn downmix_stereo(stereo: &[f32]) -> Vec<f32> {
    stereo
        .chunks_exact(2)
        .map(|f| (f[0] + f[1]) * 0.5)
        .collect()
}

/// Linear-interpolating mono resampler that keeps its fractional read
/// position and the previous input sample across calls, so consecutive IOProc
/// buffers join without a phase reset or a dropped sample at each boundary.
#[derive(Debug)]
pub struct Resampler {
    /// Input samples advanced per output sample.
    step: f64,
    /// Read position relative to the start of the next input slice; may be
    /// negative (between `previous` and the slice's first sample).
    position: f64,
    previous: Option<f32>,
}

impl Resampler {
    pub fn new(input_rate: f64, output_rate: f64) -> Self {
        let step = if input_rate > 0.0 && output_rate > 0.0 {
            input_rate / output_rate
        } else {
            1.0
        };
        Self {
            step,
            position: 0.0,
            previous: None,
        }
    }

    pub fn process(&mut self, input: &[f32]) -> Vec<f32> {
        if input.is_empty() {
            return Vec::new();
        }
        let mut out = Vec::with_capacity((input.len() as f64 / self.step) as usize + 1);
        let at = |i: isize| -> f32 {
            if i < 0 {
                self.previous.unwrap_or(input[0])
            } else {
                input[i as usize]
            }
        };
        let last = input.len() as isize - 1;
        // Emit while both interpolation neighbours are available.
        while (self.position.floor() as isize) < last {
            let base = self.position.floor();
            let frac = (self.position - base) as f32;
            let i = base as isize;
            out.push(at(i) * (1.0 - frac) + at(i + 1) * frac);
            self.position += self.step;
        }
        self.previous = input.last().copied();
        self.position -= input.len() as f64;
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn view(channels: usize, data: &[f32]) -> BufferView<'_> {
        BufferView { channels, data }
    }

    #[test]
    fn mic_and_stereo_tap_mix_into_both_channels() {
        let mic = [0.1, 0.2];
        let tap = [0.3, -0.3, 0.4, -0.4];
        let m = mix(
            &[view(1, &mic), view(2, &tap)],
            ChannelLayout { skip: 0, mic: 1 },
            1.0,
        );
        assert_eq!(m.system_channels, 2);
        let expect = [0.4, -0.2, 0.6, -0.2];
        for (a, b) in m.stereo.iter().zip(expect) {
            assert!((a - b).abs() < 1e-6, "{:?}", m.stereo);
        }
        assert!((m.mic_peak - 0.2).abs() < 1e-6);
        assert!((m.system_peak - 0.4).abs() < 1e-6);
    }

    #[test]
    fn multichannel_mic_is_averaged_and_skipped_channels_ignored() {
        let headset = [9.0, 9.0]; // clock device's own input, must not leak in
        let mic = [0.2, 0.4, 0.2, 0.4]; // two mic channels, 2 frames
        let tap = [0.0, 0.0, 0.0, 0.0];
        let m = mix(
            &[view(1, &headset), view(2, &mic), view(2, &tap)],
            ChannelLayout { skip: 1, mic: 2 },
            1.0,
        );
        assert_eq!(m.stereo.len(), 4);
        assert!(
            m.stereo.iter().all(|s| (s - 0.3).abs() < 1e-6),
            "{:?}",
            m.stereo
        );
    }

    #[test]
    fn tap_only_layout_passes_system_audio_through() {
        let tap = [0.5, -0.5];
        let m = mix(&[view(2, &tap)], ChannelLayout::default(), 1.0);
        assert_eq!(m.stereo, vec![0.5, -0.5]);
        assert_eq!(m.mic_peak, 0.0);
    }

    #[test]
    fn mono_tap_is_duplicated() {
        let tap = [0.25, 0.5];
        let m = mix(&[view(1, &tap)], ChannelLayout::default(), 1.0);
        assert_eq!(m.stereo, vec![0.25, 0.25, 0.5, 0.5]);
    }

    #[test]
    fn short_buffer_truncates_instead_of_misaligning() {
        let mic = [0.1];
        let tap = [0.2, 0.2, 0.3, 0.3];
        let m = mix(
            &[view(1, &mic), view(2, &tap)],
            ChannelLayout { skip: 0, mic: 1 },
            1.0,
        );
        assert_eq!(m.stereo.len(), 2);
    }

    #[test]
    fn a_buffer_without_data_is_silence_not_a_lost_callback() {
        let tap = [0.5, -0.5, 0.25, -0.25];
        let m = mix(
            &[view(1, &[]), view(2, &tap)],
            ChannelLayout { skip: 0, mic: 1 },
            1.0,
        );
        assert_eq!(m.stereo, vec![0.5, -0.5, 0.25, -0.25]);
        assert_eq!(m.mic_peak, 0.0);
    }

    #[test]
    fn empty_or_zero_channel_input_yields_nothing() {
        assert_eq!(mix(&[], ChannelLayout::default(), 1.0), Mixed::default());
        assert_eq!(
            mix(&[view(2, &[])], ChannelLayout::default(), 1.0),
            Mixed::default()
        );
    }

    #[test]
    fn layout_larger_than_available_channels_is_clamped() {
        let tap = [0.5, 0.5];
        let m = mix(&[view(2, &tap)], ChannelLayout { skip: 0, mic: 8 }, 1.0);
        assert_eq!(m.system_channels, 0);
        assert!(m.stereo.iter().all(|s| (s - 0.5).abs() < 1e-6));
    }

    #[test]
    fn soft_clip_is_identity_below_knee_and_bounded_above() {
        assert_eq!(soft_clip(0.5), 0.5);
        assert_eq!(soft_clip(-0.9), -0.9);
        assert!(soft_clip(0.95) < 0.95 && soft_clip(0.95) > 0.9);
        assert!(soft_clip(1.8) <= 1.0 && soft_clip(1.8) > 0.9);
        assert!(soft_clip(-5.0) >= -1.0);
        assert_eq!(soft_clip(f32::NAN), 0.0);
        assert_eq!(soft_clip(f32::INFINITY), 0.0);
    }

    #[test]
    fn downmix_averages_pairs() {
        assert_eq!(downmix_stereo(&[1.0, 0.0, 0.5, 0.5, 9.0]), vec![0.5, 0.5]);
    }

    #[test]
    fn resampler_output_length_is_stable_across_chunking() {
        let input: Vec<f32> = (0..4800).map(|i| (i as f32 * 0.01).sin()).collect();
        let mut whole = Resampler::new(48_000.0, 16_000.0);
        let one = whole.process(&input);
        let mut chunked = Resampler::new(48_000.0, 16_000.0);
        let mut many = Vec::new();
        for c in input.chunks(333) {
            many.extend(chunked.process(c));
        }
        assert!((one.len() as isize - 1600).abs() <= 1, "len {}", one.len());
        assert!((many.len() as isize - one.len() as isize).abs() <= 1);
        for (a, b) in one.iter().zip(&many) {
            assert!((a - b).abs() < 1e-5);
        }
    }

    #[test]
    fn resampler_interpolates_across_buffer_boundaries() {
        // Ramp 0,1,2,3,... split mid-way: output must stay a ramp.
        let mut r = Resampler::new(2.0, 1.0);
        let mut out = r.process(&[0.0, 1.0, 2.0]);
        out.extend(r.process(&[3.0, 4.0, 5.0]));
        assert_eq!(out, vec![0.0, 2.0, 4.0]);

        let mut up = Resampler::new(1.0, 2.0);
        let mut out = up.process(&[0.0, 1.0]);
        out.extend(up.process(&[2.0]));
        assert_eq!(out, vec![0.0, 0.5, 1.0, 1.5]);
    }

    #[test]
    fn resampler_handles_non_integer_ratio() {
        let mut r = Resampler::new(44_100.0, 16_000.0);
        let total: usize = (0..100).map(|_| r.process(&[0.0; 441]).len()).sum();
        assert!((total as isize - 16_000).abs() <= 2, "total {total}");
    }
}
