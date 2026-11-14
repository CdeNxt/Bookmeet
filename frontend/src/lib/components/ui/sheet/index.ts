import { Dialog as SheetPrimitive } from 'bits-ui';
import Content from './sheet-content.svelte';
import Overlay from './sheet-overlay.svelte';
import Title from './sheet-title.svelte';

const Root = SheetPrimitive.Root;
const Trigger = SheetPrimitive.Trigger;
const Close = SheetPrimitive.Close;
const Portal = SheetPrimitive.Portal;
const Description = SheetPrimitive.Description;

export {
	Root, Trigger, Close, Portal, Content, Overlay, Title, Description,
	Root as Sheet, Trigger as SheetTrigger, Close as SheetClose, Portal as SheetPortal,
	Content as SheetContent, Overlay as SheetOverlay, Title as SheetTitle,
	Description as SheetDescription,
};
