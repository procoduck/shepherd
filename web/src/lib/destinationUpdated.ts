/**
 * The "Destination updated" confirmation (#309). UpdateDestination regenerates
 * every wizard pipeline that ships to the destination and whose text the
 * change touches, and says how many in Destination.regenerated_pipelines.
 * Nothing regenerated reads as the plain confirmation: most edits (a rename
 * no wizard names, an extra-only change) touch no pipeline, and saying so
 * every time is noise.
 */
export function destinationUpdatedMessage(regeneratedPipelines: number): string {
  if (regeneratedPipelines <= 0) return 'Destination updated';
  const noun = regeneratedPipelines === 1 ? 'pipeline' : 'pipelines';
  return `Destination updated — ${regeneratedPipelines} ${noun} regenerated`;
}
