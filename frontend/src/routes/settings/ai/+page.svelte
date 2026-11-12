<script lang="ts">
	import { onMount } from 'svelte';
	import { onDestroy } from 'svelte';
	import { api, type LLMSettings, type AssistantSettings } from '$lib/api';
	import { ConfirmDialog } from '$lib/components/ui/confirm-dialog';
	import { currentUser } from '$lib/stores';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Switch } from '$lib/components/ui/switch';
	import { Textarea } from '$lib/components/ui/textarea';
	import { toast } from 'svelte-sonner';
	import { saveOnCmdS } from '$lib/save-shortcut';
	import { createAsyncFlag } from '$lib/async-action.svelte';

	const loadingFlag = createAsyncFlag(true);
	const savingFlag = createAsyncFlag();
	const testingFlag = createAsyncFlag();

	let settings = $state<LLMSettings | null>(null);
	let enabled = $state(false);
	let endpoint = $state('');
	let model = $state('');
	let apiKey = $state('');
	let extraInstructions = $state('');

	// Personal booking assistant (its own card, saved separately from the model settings).
	const assistantFlag = createAsyncFlag();
	let assistant = $state<AssistantSettings | null>(null);
	let botKey = $state('');
	let connectUrl = $state('');
	let confirmOpen = $state(false);
	let confirmTitle = $state('');
	let confirmDescription = $state('');
	let confirmText = $state('');
	let pendingAction = $state<(() => void) | null>(null);
	let linkPoll: ReturnType<typeof setInterval> | undefined;

	function stopLinkPoll() {
		if (linkPoll) clearInterval(linkPoll);
		linkPoll = undefined;
	}
	onDestroy(stopLinkPoll);

	async function patchAssistant(body: Record<string, unknown>, done: string) {
		await assistantFlag.run(async () => {
			assistant = await api.patch<AssistantSettings>('/v1/settings/assistant', body);
			toast.success(done);
		}, 'Could not save the assistant settings');
	}

	async function connectBot() {
		if (!botKey.trim()) {
			toast.error('Paste the bot key first');
			return;
		}
		await patchAssistant({ telegram_bot_token: botKey.trim() }, 'Telegram bot connected');
		if (assistant?.telegram.configured) botKey = '';
	}

	function askDisconnectBot() {
		confirmTitle = 'Disconnect the Telegram bot?';
		confirmDescription =
			'The assistant is switched off, the bot key is forgotten and every linked Telegram chat is unlinked.';
		confirmText = 'Disconnect';
		pendingAction = () => {
			connectUrl = '';
			stopLinkPoll();
			void patchAssistant({ telegram_bot_token: '' }, 'Telegram bot disconnected');
		};
		confirmOpen = true;
	}

	// Makes a one-time link. Opening it in Telegram and pressing Start ties that chat to this
	// account; the page then checks every few seconds until the link shows up.
	async function connectMyTelegram() {
		await assistantFlag.run(async () => {
			const res = await api.post<{ url: string; expires_minutes: number }>(
				'/v1/settings/assistant/telegram/link'
			);
			connectUrl = res.url;
			stopLinkPoll();
			let checks = 0;
			linkPoll = setInterval(async () => {
				checks += 1;
				try {
					const next = await api.get<AssistantSettings>('/v1/settings/assistant');
					if (next.link.linked) {
						assistant = next;
						connectUrl = '';
						stopLinkPoll();
						toast.success('Telegram connected');
					}
				} catch {
					// A failed check is not worth a message; the next one may succeed.
				}
				if (checks >= 60) stopLinkPoll();
			}, 3000);
		}, 'Could not make a connect link');
	}

	function askUnlink() {
		confirmTitle = 'Unlink your Telegram?';
		confirmDescription = 'The bot stops answering your chat until you connect it again.';
		confirmText = 'Unlink';
		pendingAction = () => {
			void assistantFlag.run(async () => {
				assistant = await api.del<AssistantSettings>('/v1/settings/assistant/telegram/link');
				toast.success('Telegram unlinked');
			}, 'Could not unlink Telegram');
		};
		confirmOpen = true;
	}

	onMount(() => loadingFlag.run(async () => {
		assistant = await api.get<AssistantSettings>('/v1/settings/assistant');
		settings = await api.get<LLMSettings>('/v1/settings/llm');
		enabled = settings.enabled;
		endpoint = settings.endpoint;
		model = settings.model;
		extraInstructions = settings.extra_instructions;
	}, 'Could not load AI settings'));

	async function save() {
		await savingFlag.run(async () => {
			const body: Record<string, unknown> = {
				enabled, endpoint: endpoint.trim(), model: model.trim(),
				extra_instructions: extraInstructions
			};
			if (apiKey) body.api_key = apiKey;
			settings = await api.patch<LLMSettings>('/v1/settings/llm', body);
			enabled = settings.enabled;
			extraInstructions = settings.extra_instructions;
			apiKey = '';
			// The assistant's "active" state depends on the model being live.
			assistant = await api.get<AssistantSettings>('/v1/settings/assistant');
			toast.success(settings.active ? 'Saved — AI is on' : 'Saved');
		}, 'Could not save AI settings');
	}

	async function testConnection() {
		if (!endpoint.trim() || !model.trim()) {
			toast.error('Enter an endpoint and model first');
			return;
		}
		await testingFlag.run(async () => {
			const res = await api.post<{ ok: boolean; latency_ms?: number; error?: string }>(
				'/v1/settings/llm/test',
				{ endpoint: endpoint.trim(), model: model.trim(), api_key: apiKey || undefined }
			);
			if (res.ok) toast.success(`Connection OK (${res.latency_ms} ms)`);
			else toast.error(`Test failed: ${res.error}`);
		}, 'Test request failed');
	}
</script>

<svelte:window onkeydown={saveOnCmdS(save, () => !savingFlag.active)} />

{#if !$currentUser?.is_admin}
	<p class="text-sm text-muted-foreground">Admin access required.</p>
{:else if loadingFlag.active}
	<p class="py-8 text-sm text-muted-foreground">Loading…</p>
{:else}
	<div class="max-w-lg space-y-4">
		<div class="rounded-lg border bg-card p-6">
			<div class="mb-4 flex items-start justify-between gap-2">
				<div>
					<h2 class="text-sm font-semibold">AI assistant</h2>
					<p class="mt-0.5 text-xs text-muted-foreground">
						Powers conversational booking on your booking pages. Bring your own
						model — any OpenAI-compatible endpoint (a hosted model, or one you run
						yourself). Off by default; if it's unavailable, bookers just use the calendar.
					</p>
				</div>
				{#if settings !== null}
					<span class="flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium {settings.active ? 'bg-green-50 text-green-700' : 'bg-amber-50 text-amber-700'}">
						<span class="h-1.5 w-1.5 rounded-full {settings.active ? 'bg-green-500' : 'bg-amber-400'}"></span>
						{settings.active ? 'Active' : settings.configured ? 'Configured (off)' : 'Not configured'}
					</span>
				{/if}
			</div>

			<div class="space-y-3">
				<div class="space-y-1.5">
					<Label for="ai-endpoint">API endpoint</Label>
					<Input id="ai-endpoint" type="text" placeholder="https://api.openai.com/v1" bind:value={endpoint} />
					<p class="text-xs text-muted-foreground">OpenAI-compatible base URL; the client appends <code class="rounded bg-muted px-1">/chat/completions</code>.</p>
				</div>
				<div class="space-y-1.5">
					<Label for="ai-model">Model</Label>
					<Input id="ai-model" type="text" placeholder="gpt-4o-mini / claude-haiku-4-5 / gemini-flash …" bind:value={model} />
				</div>
				<div class="space-y-1.5">
					<Label for="ai-key">API key</Label>
					<Input id="ai-key" type="password"
						placeholder={settings?.api_key_set ? '•••••••• (stored)' : 'Enter API key (optional for local endpoints)'}
						bind:value={apiKey} />
					{#if settings?.api_key_set && !apiKey}
						<p class="text-xs text-muted-foreground">Stored — leave blank to keep it.</p>
					{/if}
				</div>

				<div class="flex items-center justify-between rounded-md border bg-muted/30 px-3 py-2">
					<div>
						<Label for="ai-enabled" class="text-sm">Enable AI features</Label>
						<p class="text-xs text-muted-foreground">Turn conversational booking on for your booking pages.</p>
					</div>
					<Switch id="ai-enabled" bind:checked={enabled} />
				</div>
			</div>

			<div class="mt-5 flex gap-2">
				<Button onclick={save} disabled={savingFlag.active}>{savingFlag.active ? 'Saving…' : 'Save'}</Button>
				<Button variant="outline" onclick={testConnection} disabled={testingFlag.active}>
					{testingFlag.active ? 'Testing…' : 'Test connection'}
				</Button>
			</div>
		</div>

		{#if assistant !== null}
			<div class="rounded-lg border bg-card p-6">
				<div class="mb-4 flex items-start justify-between gap-2">
					<div>
						<h2 class="text-sm font-semibold">Personal booking assistant</h2>
						<p class="mt-0.5 text-xs text-muted-foreground">
							Message a Telegram bot to set up a meeting. It makes a personal booking link
							for your guest and tells you what happens. It needs the AI model above.
						</p>
					</div>
					<span class="flex shrink-0 items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium {assistant.active ? 'bg-green-50 text-green-700' : 'bg-amber-50 text-amber-700'}">
						<span class="h-1.5 w-1.5 rounded-full {assistant.active ? 'bg-green-500' : 'bg-amber-400'}"></span>
						{assistant.active ? 'Active' : assistant.enabled ? 'On, not ready' : 'Off'}
					</span>
				</div>

				<div class="space-y-4">
					<div class="space-y-1.5">
						<Label for="assistant-bot-key">Telegram bot key</Label>
						<Input id="assistant-bot-key" type="password" autocomplete="off"
							placeholder={assistant.telegram.configured ? '•••••••• (stored)' : 'Paste the key from @BotFather'}
							bind:value={botKey} />
						<p class="text-xs text-muted-foreground">
							{#if assistant.telegram.configured}
								Connected as <span class="font-medium text-foreground">@{assistant.telegram.bot_username}</span>.
								Paste a new key only to replace it.
							{:else}
								In Telegram, message @BotFather, send /newbot, and paste the key it gives you here.
							{/if}
						</p>
						<div class="flex gap-2 pt-1">
							<Button size="sm" onclick={connectBot} disabled={assistantFlag.active}>
								{assistant.telegram.configured ? 'Replace key' : 'Connect bot'}
							</Button>
							{#if assistant.telegram.configured}
								<Button size="sm" variant="outline" onclick={askDisconnectBot} disabled={assistantFlag.active}>
									Disconnect bot
								</Button>
							{/if}
						</div>
					</div>

					{#if assistant.telegram.configured}
						<div class="space-y-1.5 border-t pt-4">
							<p class="text-sm font-medium">Your Telegram</p>
							{#if assistant.link.linked}
								<p class="text-xs text-muted-foreground">
									Linked{assistant.link.telegram_name ? ` as ${assistant.link.telegram_name}` : ''}.
									The bot answers only this chat.
								</p>
								<Button size="sm" variant="outline" onclick={askUnlink} disabled={assistantFlag.active}>Unlink</Button>
							{:else}
								<p class="text-xs text-muted-foreground">
									Link your own Telegram chat so the bot knows it is you. It ignores everyone else.
								</p>
								<Button size="sm" onclick={connectMyTelegram} disabled={assistantFlag.active}>
									{connectUrl ? 'Make a new link' : 'Connect my Telegram'}
								</Button>
								{#if connectUrl}
									<p class="text-xs text-muted-foreground">
										<a class="font-medium text-foreground underline" href={connectUrl} target="_blank" rel="noopener noreferrer">Open the bot in Telegram</a>
										and press Start. The link works once and expires in 15 minutes. This page updates when it's done.
									</p>
								{/if}
							{/if}
						</div>
					{/if}

					<div class="flex items-center justify-between rounded-md border bg-muted/30 px-3 py-2">
						<div>
							<Label for="assistant-enabled" class="text-sm">Assistant on</Label>
							<p class="text-xs text-muted-foreground">
								{#if !assistant.telegram.configured}
									Connect a Telegram bot first.
								{:else if !assistant.ai_active}
									Set up and enable the AI model above first.
								{:else}
									When off, the bot goes quiet.
								{/if}
							</p>
						</div>
						<Switch id="assistant-enabled" checked={assistant.enabled}
							disabled={assistantFlag.active || (!assistant.enabled && (!assistant.telegram.configured || !assistant.ai_active))}
							onCheckedChange={(v) => patchAssistant({ enabled: v }, v ? 'Assistant switched on' : 'Assistant switched off')} />
					</div>
				</div>
			</div>
		{/if}

		<div class="rounded-lg border bg-card p-6">
			<h2 class="text-sm font-semibold">Assistant instructions</h2>
			<p class="mt-0.5 text-xs text-muted-foreground">
				Extra guidance — tone, business context, do's and don'ts. Appended to the built-in
				instructions (which handle the booking flow + safety and can't be edited).
			</p>
			<div class="mt-3 space-y-1.5">
				<Label for="ai-extra">Additional instructions</Label>
				<Textarea id="ai-extra" rows={4} bind:value={extraInstructions}
					placeholder="e.g. Keep a warm, professional tone. We're a law firm — mention that consultations are confidential." />
				<p class="text-xs text-muted-foreground">
					The assistant replies in each visitor's own language automatically. Writing these
					instructions in a different language than your visitors works, but is somewhat less
					reliable — for best results, write them in whichever language most of your visitors use.
				</p>
			</div>
			{#if settings?.base_prompt}
				<details class="mt-4">
					<summary class="cursor-pointer text-xs font-medium text-muted-foreground">View built-in base instructions (read-only)</summary>
					<pre class="mt-2 max-h-64 overflow-auto whitespace-pre-wrap rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">{settings.base_prompt}</pre>
				</details>
			{/if}
			<div class="mt-5">
				<Button onclick={save} disabled={savingFlag.active}>{savingFlag.active ? 'Saving…' : 'Save'}</Button>
			</div>
		</div>

		<p class="text-xs text-muted-foreground">
			Privacy: the assistant only ever receives computed availability windows and public
			event details — never your calendar event titles, attendees, or other private data.
			For strict data-residency needs, point the endpoint at inference you control.
		</p>
	</div>
{/if}

<ConfirmDialog
	bind:open={confirmOpen}
	title={confirmTitle}
	description={confirmDescription}
	confirmText={confirmText}
	destructive
	onConfirm={() => pendingAction?.()}
/>
