import React from 'react';

/**
 * Modal overlay host for the artifact/audit inspector. Renders a dimmed
 * backdrop with a large centered dialog surface, visibly inset from the
 * window edges so it reads as a pop-up window rather than a full-screen
 * route. ArtifactPanel (the child) supplies the header and its own close
 * button. On small screens the surface is nearly full-bleed but keeps its
 * margin, radius, and border so it still reads as a modal.
 *
 * Modal contract: dialog semantics, Escape or a backdrop click closes, focus
 * moves into the dialog on open and returns to the previously focused element
 * on close. Tab is not forcibly cycled — aria-modal hides the background tree
 * from assistive tech.
 */
const InspectorOverlay: React.FC<{
  onClose: () => void;
  children: React.ReactNode;
}> = ({ onClose, children }) => {
  const containerRef = React.useRef<HTMLDivElement | null>(null);
  const onCloseRef = React.useRef(onClose);
  onCloseRef.current = onClose;

  React.useEffect(() => {
    const previouslyFocused =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    containerRef.current?.focus({ preventScroll: true });
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onCloseRef.current();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => {
      window.removeEventListener('keydown', onKeyDown);
      previouslyFocused?.focus({ preventScroll: true });
    };
  }, []);

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-3 sm:p-6 lg:p-10">
      {/* The surface is a sibling (not a child) of the backdrop, so clicks
          inside the dialog can never bubble into this close handler. */}
      <div
        aria-hidden="true"
        data-testid="inspector-backdrop"
        onClick={() => onCloseRef.current()}
        className="absolute inset-0 bg-black/80 motion-safe:animate-in motion-safe:fade-in motion-safe:duration-200"
      />
      <div
        ref={containerRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label="Artifact inspector"
        data-testid="inspector-overlay"
        className="relative flex h-full w-full max-w-screen-2xl flex-col overflow-hidden rounded-xl border border-border bg-background shadow-2xl outline-none motion-safe:animate-in motion-safe:fade-in motion-safe:zoom-in-95 motion-safe:duration-200"
      >
        {children}
      </div>
    </div>
  );
};

export default InspectorOverlay;
