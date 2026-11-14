<script lang="ts">
	import { page } from '$app/stores';
	import { base } from '$app/paths';
	import { goto } from '$app/navigation';
	import * as Select from '$lib/components/ui/select';
	import { currentUser } from '$lib/stores';
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();

	const navItems = [
		{ section: 'Your account', href: `${base}/settings/profile`, label: 'Profile' },
		{ section: 'Your account', href: `${base}/settings/notifications`, label: 'Notifications' },
		{ section: 'Workspace', href: `${base}/settings/branding`, label: 'Branding', adminOnly: true },
		{ section: 'Workspace', href: `${base}/settings/email`, label: 'Email', adminOnly: true },
		{ section: 'Workspace', href: `${base}/settings/google`, label: 'Google OAuth', adminOnly: true },
		{ section: 'Workspace', href: `${base}/settings/zoom`, label: 'Zoom', adminOnly: true },
		{ section: 'Workspace', href: `${base}/settings/video`, label: 'Video', adminOnly: true },
		{ section: 'Workspace', href: `${base}/settings/storage`, label: 'Storage', adminOnly: true },
		{ section: 'Workspace', href: `${base}/settings/payments`, label: 'Payments', adminOnly: true },
		{ section: 'Workspace', href: `${base}/settings/ai`, label: 'AI', adminOnly: true },
		{ section: 'Workspace', href: `${base}/settings/tracking`, label: 'Tracking', adminOnly: true },
	];

	const visibleNavItems = $derived(navItems.filter((item) => !item.adminOnly || $currentUser?.is_admin));
	const sections = $derived([...new Set(visibleNavItems.map((item) => item.section))]);
	const current = $derived(visibleNavItems.find((item) => $page.url.pathname.startsWith(item.href)));
</script>

<svelte:head><title>Settings — Bookmeet</title></svelte:head>

<div class="mb-5 md:mb-8">
	<h1 class="text-2xl font-semibold tracking-tight">Settings</h1>
	<p class="mt-1 text-sm text-muted-foreground">Manage your profile, preferences, and integrations.</p>
</div>

<!-- Phone: the sections are one full-width picker above the page, so the page keeps the
     whole width. From `md` up they are the side column, as before. -->
<div class="mb-5 md:hidden">
	<Select.Root type="single" value={current?.href} onValueChange={(v) => { if (v && v !== current?.href) goto(v); }}>
		<Select.Trigger class="h-11 w-full text-base font-medium" aria-label="Settings section">
			{current?.label ?? 'Choose a section'}
		</Select.Trigger>
		<Select.Content class="w-(--bits-select-anchor-width) max-h-[min(70vh,var(--bits-select-content-available-height,100vh))]">
			{#each sections as section}
				<Select.Group>
					<Select.GroupHeading>{section}</Select.GroupHeading>
					{#each visibleNavItems.filter((item) => item.section === section) as item}
						<Select.Item value={item.href} label={item.label} class="py-2.5 text-base">{item.label}</Select.Item>
					{/each}
				</Select.Group>
			{/each}
		</Select.Content>
	</Select.Root>
</div>

<div class="flex gap-8">
	<nav class="hidden w-44 shrink-0 md:block">
		<ul class="space-y-0.5">
			{#each visibleNavItems as item, i}
				{#if item.section !== visibleNavItems[i - 1]?.section}
					<li class="mb-1 px-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground/60 first:mt-0 mt-4">
						{item.section}
					</li>
				{/if}
				{@const active = $page.url.pathname.startsWith(item.href)}
				<li>
					<a
						href={item.href}
						class="block rounded-md px-3 py-2 text-sm font-medium transition-colors
							{active
								? 'bg-accent text-accent-foreground'
								: 'text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground'}"
					>
						{item.label}
					</a>
				</li>
			{/each}
		</ul>
	</nav>

	<div class="min-w-0 flex-1">
		{@render children()}
	</div>
</div>
