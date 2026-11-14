<script lang="ts" module>
	import { tv, type VariantProps } from 'tailwind-variants';

	// A panel that slides in from one edge. Built on the bits-ui Dialog, so it traps
	// focus, closes on Escape and on a tap outside, and locks the page behind it.
	// The open/closed animations hang off `data-state`, written in the explicit
	// `data-[state=…]` form (see TESTING.md for why the short form needs a remap).
	export const sheetVariants = tv({
		base: 'fixed z-50 flex flex-col bg-background shadow-lg transition ease-in-out data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:duration-200 data-[state=open]:duration-300',
		variants: {
			side: {
				top: 'inset-x-0 top-0 h-auto border-b data-[state=closed]:slide-out-to-top data-[state=open]:slide-in-from-top',
				bottom: 'inset-x-0 bottom-0 h-auto border-t data-[state=closed]:slide-out-to-bottom data-[state=open]:slide-in-from-bottom',
				left: 'inset-y-0 left-0 h-full w-3/4 border-r data-[state=closed]:slide-out-to-left data-[state=open]:slide-in-from-left sm:max-w-sm',
				right: 'inset-y-0 right-0 h-full w-3/4 border-l data-[state=closed]:slide-out-to-right data-[state=open]:slide-in-from-right sm:max-w-sm'
			}
		},
		defaultVariants: { side: 'right' }
	});

	export type Side = VariantProps<typeof sheetVariants>['side'];
</script>

<script lang="ts">
	import { Dialog as SheetPrimitive } from 'bits-ui';
	import { cn } from '$lib/utils.js';
	import Overlay from './sheet-overlay.svelte';

	let {
		ref = $bindable(null),
		class: className,
		side = 'right',
		showClose = true,
		children,
		...restProps
	}: SheetPrimitive.ContentProps & { side?: Side; showClose?: boolean } = $props();
</script>

<SheetPrimitive.Portal>
	<Overlay />
	<SheetPrimitive.Content
		bind:ref
		data-slot="sheet-content"
		class={cn(sheetVariants({ side }), className)}
		{...restProps}
	>
		{@render children?.()}
		{#if showClose}
			<SheetPrimitive.Close class="absolute right-3 top-3 flex size-9 items-center justify-center rounded-md opacity-70 transition-opacity hover:opacity-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none">
				<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
				<span class="sr-only">Close</span>
			</SheetPrimitive.Close>
		{/if}
	</SheetPrimitive.Content>
</SheetPrimitive.Portal>
