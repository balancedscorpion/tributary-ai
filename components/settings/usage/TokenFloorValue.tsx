import React from 'react';
import { tokenValueSrText } from './usageFormat';

/**
 * A formatted token count whose floor and unknown glyphs remain meaningful
 * when a screen reader's punctuation level suppresses `≥` or `—`.
 */
export function TokenFloorValue({
  display,
  testId,
  className,
}: {
  display: string;
  testId?: string;
  className?: string;
}) {
  const srText = tokenValueSrText(display);
  if (!srText) {
    return <span className={className} data-testid={testId}>{display}</span>;
  }
  return (
    <>
      <span className={className} data-testid={testId} aria-hidden>{display}</span>
      <span className="sr-only">{srText}</span>
    </>
  );
}
