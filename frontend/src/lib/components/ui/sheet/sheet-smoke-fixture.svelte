<script lang="ts">
	// Mounted only by src/visual-smoke.test.ts: an open left-hand sheet, so the test can
	// read the computed styles that depend on `data-state`.
	import * as Sheet from './index.js';
</script>

<Sheet.Root open={true}>
	<Sheet.Content side="left" aria-describedby={undefined}>
		<Sheet.Title>Menu</Sheet.Title>
	</Sheet.Content>
</Sheet.Root>
