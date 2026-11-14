<script lang="ts">
	// The owner's main booking page link, as a quiet pill in the top-right corner of every
	// admin page: one click copies it. The layout places it (the top bar on a phone). The main page is /u/<handle>; without a handle there
	// is no such page yet, so the pill links to where one is chosen instead.
	import { base } from '$app/paths';
	import { currentUser } from '$lib/stores';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import { toast } from 'svelte-sonner';

	const handle = $derived(($currentUser?.handle ?? '').trim());
	const origin = typeof window !== 'undefined' ? window.location.origin : '';
	const url = $derived(handle && origin ? `${origin}/u/${handle}` : '');
	// Shown without the scheme: shorter, and the scheme is not what anyone reads.
	const shown = $derived(url.replace(/^https?:\/\//, ''));
	let copied = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;
	const pill = 'h-8 min-w-0 gap-1.5 md:h-7 rounded-full px-3 text-xs font-normal text-muted-foreground hover:text-foreground';

	async function copy() {
		try {
			await navigator.clipboard.writeText(url);
			copied = true;
			clearTimeout(timer);
			timer = setTimeout(() => (copied = false), 1600);
		} catch {
			toast.error('Could not copy. Your booking page is ' + url);
		}
	}
</script>

{#if url}
	<Button
		variant="outline"
		size="sm"
		class={pill}
		onclick={copy}
		id="booking-link-pill"
		title="Copy your booking page link"
		aria-label={copied ? 'Booking page link copied' : `Copy your booking page link, ${shown}`}
	>
		<span class="size-1.5 shrink-0 rounded-full bg-emerald-500" aria-hidden="true"></span>
		<span class="max-w-[11rem] truncate sm:max-w-[16rem] font-mono">{copied ? 'Copied' : shown}</span>
		<svg class="size-3.5 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
			{#if copied}
				<path d="M5 12.5l4.5 4.5L19 7.5" />
			{:else}
				<rect x="9" y="9" width="11" height="11" rx="2" /><path d="M5 15V6a2 2 0 0 1 2-2h9" />
			{/if}
		</svg>
	</Button>
{:else}
	<a href={`${base}/settings/profile`} class={buttonVariants({ variant: 'outline', size: 'sm' }) + ' ' + pill} id="booking-link-pill">
		Set a booking handle to get your link
	</a>
{/if}
