<script lang="ts">
	import { onMount } from 'svelte';
	import { base } from '$app/paths';
	import { api, type PersonalLink } from '$lib/api';
	import { fmtDateTime, fmtTime } from '$lib/prefs';
	import { buttonVariants } from '$lib/components/ui/button';
	import { ConfirmDialog } from '$lib/components/ui/confirm-dialog';
	import { Input } from '$lib/components/ui/input';
	import * as Tooltip from '$lib/components/ui/tooltip';
	import { toast } from 'svelte-sonner';

	let loading = $state(true);
	let links = $state<PersonalLink[]>([]);
	let query = $state('');
	let cancelOpen = $state(false);
	let pendingCancel = $state<PersonalLink | null>(null);

	const statusLabel: Record<string, string> = {
		open: 'Open',
		proposed: 'Waiting on you',
		followup: 'Guest will follow up',
		booked: 'Booked',
		expired: 'Ran out',
		cancelled: 'Cancelled',
		replaced: 'Replaced'
	};
	const statusStyle: Record<string, string> = {
		open: 'bg-blue-50 text-blue-700',
		proposed: 'bg-amber-50 text-amber-700',
		followup: 'bg-blue-50 text-blue-700',
		booked: 'bg-green-50 text-green-700'
	};

	const filtered = $derived(
		query.trim()
			? links.filter((l) => {
					const q = query.toLowerCase();
					return (
						l.guest_name.toLowerCase().includes(q) ||
						l.guest_email.toLowerCase().includes(q) ||
						l.event_type_name.toLowerCase().includes(q) ||
						l.topic.toLowerCase().includes(q) ||
						(statusLabel[l.status] ?? l.status).toLowerCase().includes(q)
					);
				})
			: links
	);

	async function load() {
		const res = await api.get<{ links: PersonalLink[] }>('/v1/personal-links');
		links = res.links ?? [];
	}

	onMount(async () => {
		try {
			await load();
		} catch (e: any) {
			toast.error(e.message || 'Could not load personal links');
		} finally {
			loading = false;
		}
	});

	// "Tue 17 Nov, 9:00 AM – 10:30 AM", one per window; a long offer is cut short.
	function offer(l: PersonalLink): string {
		const parts = l.windows.slice(0, 3).map((w) => `${fmtDateTime(w.start)} – ${fmtTime(w.end)}`);
		if (l.windows.length > 3) parts.push(`${l.windows.length - 3} more`);
		return parts.join(' · ');
	}

	function progress(l: PersonalLink): string {
		if (l.status === 'booked') {
			return l.booked_start ? `Booked for ${fmtDateTime(l.booked_start)}` : 'Booked';
		}
		if (!l.last_opened_at) return 'Not opened yet';
		return `Last opened ${fmtDateTime(l.last_opened_at)}`;
	}

	async function copyLink(l: PersonalLink) {
		try {
			const res = await api.get<{ url: string }>(`/v1/personal-links/${l.id}/url`);
			await navigator.clipboard.writeText(res.url);
			toast.success(`Link for ${l.guest_name} copied`);
		} catch (e: any) {
			toast.error(e.message || 'Could not copy the link');
		}
	}

	function askCancel(l: PersonalLink) {
		pendingCancel = l;
		cancelOpen = true;
	}

	async function doCancel() {
		const l = pendingCancel;
		if (!l) return;
		try {
			await api.del(`/v1/personal-links/${l.id}`);
			toast.success('Link cancelled');
		} catch (e: any) {
			toast.error(e.message || 'Could not cancel the link');
		}
		try {
			await load();
		} catch {
			// The list is stale until the next visit; the toast above already said what happened.
		}
	}
</script>

<svelte:head><title>Personal links — Bookmeet</title></svelte:head>

<div class="mb-8">
	<h1 class="text-2xl font-semibold tracking-tight">Personal links</h1>
	<p class="mt-1 text-sm text-muted-foreground">
		Booking links your assistant made for one guest each. Copy one to send it again, or cancel it. To
		make or change a link, message your assistant on Telegram.
	</p>
</div>

{#if loading}
	<p class="py-8 text-sm text-muted-foreground">Loading…</p>
{:else if links.length === 0}
	<div class="rounded-lg border bg-card p-8 text-center">
		<p class="text-sm font-medium">No personal links yet</p>
		<p class="mt-1 text-sm text-muted-foreground">
			Ask your assistant on Telegram to set up a meeting with someone and the link appears here. The
			assistant is switched on in
			<a href="{base}/settings/ai" class="font-medium text-primary underline underline-offset-2">Settings → AI</a>.
		</p>
	</div>
{:else}
	<div class="mb-4">
		<Input type="search" placeholder="Search by guest, meeting or status…" bind:value={query} class="max-w-sm" />
	</div>
	{#if filtered.length === 0}
		<div class="rounded-lg border bg-card p-8 text-center">
			<p class="text-sm text-muted-foreground">No links match “{query}”.</p>
		</div>
	{:else}
		<div class="divide-y rounded-lg border bg-card">
			{#each filtered as l (l.id)}
				<div class="flex items-center justify-between gap-4 p-4">
					<div class="min-w-0">
						<p class="truncate font-medium">
							{l.guest_name}
							<span class="font-normal text-muted-foreground">· {l.event_type_name || 'Meeting'}{l.topic ? ` · ${l.topic}` : ''}</span>
						</p>
						<p class="mt-0.5 truncate text-xs text-muted-foreground">
							{offer(l)}{l.place ? ` · ${l.place}` : ''}{l.outside_availability ? ' · outside your usual hours' : ''}
						</p>
						<p class="mt-0.5 truncate text-xs text-muted-foreground">
							{progress(l)}{l.guest_email ? ` · ${l.guest_email}` : ''}{l.guest_phone ? ` · ${l.guest_phone}` : ''}
						</p>
					</div>
					<div class="flex shrink-0 items-center gap-1">
						<span class="mr-2 rounded-full px-2 py-0.5 text-xs font-medium {statusStyle[l.status] ?? 'bg-muted text-muted-foreground'}">
							{statusLabel[l.status] ?? l.status}
						</span>
						<Tooltip.Provider>
							<Tooltip.Root>
								<Tooltip.Trigger
									class={buttonVariants({ variant: 'ghost', size: 'icon' })}
									disabled={!l.active}
									aria-label="Copy link"
									onclick={() => copyLink(l)}
								>
									<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
								</Tooltip.Trigger>
								<Tooltip.Content>{l.active ? 'Copy link' : 'This link no longer works'}</Tooltip.Content>
							</Tooltip.Root>
						</Tooltip.Provider>
						<Tooltip.Provider>
							<Tooltip.Root>
								<Tooltip.Trigger
									class={buttonVariants({ variant: 'ghost', size: 'icon' })}
									disabled={!l.active}
									aria-label="Cancel link"
									onclick={() => askCancel(l)}
								>
									<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="4.93" y1="4.93" x2="19.07" y2="19.07"/></svg>
								</Tooltip.Trigger>
								<Tooltip.Content>{l.active ? 'Cancel link' : 'Nothing to cancel'}</Tooltip.Content>
							</Tooltip.Root>
						</Tooltip.Provider>
					</div>
				</div>
			{/each}
		</div>
	{/if}
{/if}

<ConfirmDialog
	bind:open={cancelOpen}
	title="Cancel this link?"
	description={`${pendingCancel?.guest_name ?? 'The guest'} will no longer be able to book with it. It stays in this list as cancelled. This cannot be undone; your assistant can make a new link.`}
	confirmText="Cancel link"
	cancelText="Keep it"
	destructive
	onConfirm={doCancel}
/>
