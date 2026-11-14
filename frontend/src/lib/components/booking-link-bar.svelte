<script lang="ts">
	// The owner's main booking page link, pinned to the top of every admin page so it is
	// always one click from the clipboard. The main page is /u/<handle>; without a handle
	// there is no such page yet, so the bar says how to get one instead of showing nothing.
	import { base } from '$app/paths';
	import { currentUser } from '$lib/stores';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import { toast } from 'svelte-sonner';

	const handle = $derived(($currentUser?.handle ?? '').trim());
	const url = $derived(
		handle && typeof window !== 'undefined' ? `${window.location.origin}/u/${handle}` : ''
	);
	let copied = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;

	async function copy() {
		try {
			await navigator.clipboard.writeText(url);
			copied = true;
			clearTimeout(timer);
			timer = setTimeout(() => (copied = false), 2000);
		} catch {
			toast.error('Could not copy. Select the link and copy it by hand.');
		}
	}
</script>

<div class="sticky top-0 z-20 border-b bg-background/95 backdrop-blur">
	<div class="mx-auto flex max-w-4xl flex-wrap items-center gap-x-3 gap-y-1.5 px-8 py-2.5 text-sm">
		<span class="shrink-0 font-medium">Your booking page</span>
		{#if url}
			<a
				href={url}
				target="_blank"
				rel="noopener noreferrer"
				class="min-w-0 flex-1 truncate font-mono text-xs text-primary hover:underline"
				id="booking-link-url">{url}</a
			>
			<Button variant="outline" size="sm" onclick={copy} aria-live="polite">
				{copied ? 'Copied' : 'Copy link'}
			</Button>
		{:else}
			<span class="min-w-0 flex-1 text-xs text-muted-foreground">
				Choose a booking handle to get one link that lists all your meetings.
			</span>
			<a href={`${base}/settings/profile`} class={buttonVariants({ variant: 'outline', size: 'sm' })}>
				Set handle
			</a>
		{/if}
	</div>
</div>
