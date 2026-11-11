<script lang="ts">
	// Full-page frame for the signed-out admin pages (claim, sign in, invite, password reset).
	// It mirrors the public booking skin (internal/handler/assets/bookmeet-skin.html): the same
	// dot-field painter, the same Light/Dark/Auto choice (shared `bookmeet-appearance` key), and
	// the same panel treatment, so these pages match what visitors see in production.
	import { onMount } from 'svelte';
	import type { Snippet } from 'svelte';
	import BookmeetLogo from './bookmeet-logo.svelte';

	let { children, logo = true }: { children: Snippet; logo?: boolean } = $props();

	let canvas: HTMLCanvasElement;
	let dark = $state(true);

	onMount(() => {
		const system = matchMedia('(prefers-color-scheme: dark)');
		let choice = 'auto';
		try {
			const saved = localStorage.getItem('bookmeet-appearance');
			if (saved === 'light' || saved === 'dark') choice = saved;
		} catch {
			// storage unavailable: follow the device
		}
		const applyTheme = () => {
			dark = choice === 'auto' ? system.matches : choice === 'dark';
		};
		applyTheme();
		system.addEventListener('change', applyTheme);

		// Painter: kept in step with the public skin's. Light is the CSS inverse of this canvas.
		const ctx = canvas.getContext('2d');
		if (!ctx) return () => system.removeEventListener('change', applyTheme);
		const reduced = matchMedia('(prefers-reduced-motion: reduce)');
		let width = innerWidth + 40, height = innerHeight + 40, raf = 0, last = 0;
		const start = performance.now();
		let px = 0, py = 0, tx = 0, ty = 0, pointerX = -1000, pointerY = -1000;
		const stars = Array.from({ length: 520 }, (_, i) => ({
			x: ((i * 7919) % 997) / 997,
			y: ((i * 6271) % 991) / 991,
			a: 0.08 + ((i * 13) % 13) / 100
		}));
		function paint(time: number) {
			if (!ctx) return;
			px += (tx - px) * 0.055;
			py += (ty - py) * 0.055;
			canvas.style.transform = `translate3d(${(-px * 10).toFixed(2)}px,${(-py * 7).toFixed(2)}px,0)`;
			ctx.fillStyle = '#15161e';
			ctx.fillRect(0, 0, width, height);
			for (const s of stars) {
				ctx.fillStyle = `rgba(188,195,220,${s.a})`;
				ctx.fillRect(s.x * width + px * 9, s.y * height + py * 6, 1, 1);
			}
			const mobile = width < 741, planes = mobile ? 30 : 48;
			const half = Math.max(width * 0.43, mobile ? 260 : 440), depth = half * 0.33;
			for (let j = 0; j < planes; j++) {
				const z = (j / (planes - 1) - 0.5) * 2;
				const angle = z * 0.64 + 0.28 * Math.sin(z * 2.1 + time) + time * 0.08 + px * 0.22;
				const span = half * (1 + 0.14 * Math.abs(z)), cy = height * 0.48 + z * height * 0.7 + py * 20;
				const corners = [[-span, -depth], [span, -depth], [span, depth], [-span, depth]].map(([x, y]) => [
					width * 0.5 + x * Math.cos(angle) - y * Math.sin(angle) + px * 45,
					cy + (x * Math.sin(angle) + y * Math.cos(angle)) * (0.47 + py * 0.025)
				]);
				for (let k = 0; k < 4; k++) {
					const a = corners[k], b = corners[(k + 1) % 4];
					const n = Math.min(mobile ? 78 : 125, Math.max(10, Math.floor(Math.hypot(b[0] - a[0], b[1] - a[1]) / 4)));
					for (let q = 0; q < n; q++) {
						const f = q / n;
						let x = a[0] + (b[0] - a[0]) * f, y = a[1] + (b[1] - a[1]) * f;
						const dx = x - pointerX, dy = y - pointerY, dist = Math.hypot(dx, dy), near = Math.max(0, 1 - dist / 200);
						if (dist > 1 && near > 0) { x += (dx / dist) * near * 14; y += (dy / dist) * near * 14; }
						const quiet = Math.min(1, 0.48 + Math.abs(x - width * 0.5) / (width * 0.38));
						const alpha = (0.2 + 0.24 * (0.5 + 0.5 * Math.sin(q * 0.87 + j * 1.4 + time * 2)) + near * 0.24) * quiet;
						ctx.fillStyle = `rgba(188,194,220,${alpha})`;
						const size = near > 0.1 ? 1.15 : 0.8;
						ctx.fillRect(x, y, size, size);
					}
				}
			}
			const shade = ctx.createRadialGradient(width * 0.5, height * 0.34, 0, width * 0.5, height * 0.34, width * 0.54);
			shade.addColorStop(0, 'rgba(21,22,30,.34)');
			shade.addColorStop(1, 'rgba(21,22,30,0)');
			ctx.fillStyle = shade;
			ctx.fillRect(0, 0, width, height);
		}
		const now = () => (performance.now() - start) * 0.00012;
		function resize() {
			if (!ctx) return;
			width = innerWidth + 40;
			height = innerHeight + 40;
			const d = Math.min(devicePixelRatio || 1, 1.5);
			canvas.width = Math.round(width * d);
			canvas.height = Math.round(height * d);
			ctx.setTransform(d, 0, 0, d, 0, 0);
			paint(reduced.matches ? 0 : now());
		}
		function frame(t: number) {
			raf = 0;
			if (document.hidden || reduced.matches) return;
			if (t - last > 40) { last = t; paint((t - start) * 0.00012); }
			raf = requestAnimationFrame(frame);
		}
		const resume = () => { if (!document.hidden && !reduced.matches && !raf) raf = requestAnimationFrame(frame); };
		const stop = () => { if (raf) { cancelAnimationFrame(raf); raf = 0; } };
		const onMove = (e: PointerEvent) => {
			if (reduced.matches || e.pointerType === 'touch') return;
			tx = (e.clientX / innerWidth - 0.5) * 2;
			ty = (e.clientY / innerHeight - 0.5) * 2;
			pointerX = e.clientX + 20;
			pointerY = e.clientY + 20;
		};
		const onLeave = () => { tx = 0; ty = 0; pointerX = -1000; pointerY = -1000; };
		const onVisibility = () => (document.hidden ? stop() : resume());
		const onReduced = () => { stop(); onLeave(); px = py = 0; paint(0); resume(); };

		addEventListener('resize', resize, { passive: true });
		addEventListener('pointermove', onMove, { passive: true });
		document.addEventListener('pointerleave', onLeave);
		document.addEventListener('visibilitychange', onVisibility);
		reduced.addEventListener('change', onReduced);
		resize();
		resume();

		return () => {
			stop();
			system.removeEventListener('change', applyTheme);
			removeEventListener('resize', resize);
			removeEventListener('pointermove', onMove);
			document.removeEventListener('pointerleave', onLeave);
			document.removeEventListener('visibilitychange', onVisibility);
			reduced.removeEventListener('change', onReduced);
		};
	});
</script>

<div class="auth-shell" class:dark data-theme={dark ? 'dark' : 'light'}>
	<canvas bind:this={canvas} class="auth-field" aria-hidden="true"></canvas>
	<div class="auth-content">
		<div class="w-full max-w-sm">
			{#if logo}
				<div class="mb-6 flex justify-center">
					<BookmeetLogo class="h-14 w-auto text-foreground" />
				</div>
			{/if}
			<div class="auth-card text-foreground">
				{@render children()}
			</div>
		</div>
	</div>
</div>

<style>
	.auth-shell {
		position: relative;
		min-height: 100vh;
		min-height: 100svh;
		overflow: hidden;
		background: #eae9e1;
		color-scheme: light;
		/* The public skin's accent, so the main button matches the booking pages. */
		--primary: #5965e9;
		--primary-foreground: #fff;
		--ring: #939dff;
	}
	.auth-shell.dark {
		background: #15161e;
		color-scheme: dark;
	}
	.auth-field {
		position: fixed;
		inset: -20px;
		width: calc(100% + 40px);
		height: calc(100% + 40px);
		z-index: 0;
		pointer-events: none;
		opacity: 0.82;
		filter: invert(1);
	}
	.auth-shell.dark .auth-field {
		filter: none;
	}
	.auth-content {
		position: relative;
		z-index: 1;
		display: flex;
		min-height: 100vh;
		min-height: 100svh;
		align-items: center;
		justify-content: center;
		padding: 24px 16px;
	}
	.auth-card {
		background: #fff;
		border: 1px solid #d8dbe6;
		border-radius: 20px;
		box-shadow: 0 20px 80px #0000001f;
		padding: 28px 24px;
	}
	.auth-shell.dark .auth-card {
		background: rgba(25, 27, 38, 0.96);
		border-color: #ffffff20;
		box-shadow: 0 20px 80px #0003, 0 1px 0 #ffffff0d inset;
	}
</style>
