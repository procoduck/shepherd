import { autocompletion, CompletionContext, type CompletionResult } from '@codemirror/autocomplete';
import { alloySchema } from './alloySchema';

/**
 * alloyCompletionSource provides context-sensitive autocomplete for Alloy config.
 * It covers component/block declarations, attributes, values, and exported refs.
 */
export function alloyCompletionSource(ctx: CompletionContext): CompletionResult | null {
  const tokenBefore = ctx.tokenBefore(['String', 'LineComment', 'BlockComment']);
  if (tokenBefore && ['String', 'LineComment', 'BlockComment'].includes(tokenBefore.type.name))
    return null;

  const docText = ctx.state.doc.toString();
  const textBefore = docText.slice(0, ctx.pos);
  let depth = 0;
  let inString = false;
  let stringChar = '';
  let inLineComment = false;
  let currentBlockHeader = '';

  for (let i = 0; i < textBefore.length; i++) {
    const ch = textBefore[i];
    if (inLineComment) {
      if (ch === '\n') inLineComment = false;
      continue;
    }
    if (inString) {
      if (ch === stringChar && textBefore[i - 1] !== '\\') inString = false;
      continue;
    }
    if (ch === '"' || ch === '`') {
      inString = true;
      stringChar = ch;
      continue;
    }
    if (ch === '/' && textBefore[i + 1] === '/') {
      inLineComment = true;
      continue;
    }
    if (ch === '{') {
      const lineStart = textBefore.lastIndexOf('\n', i - 1) + 1;
      currentBlockHeader = textBefore.slice(lineStart, i).trim();
      depth++;
    } else if (ch === '}') {
      depth--;
      if (depth <= 0) currentBlockHeader = '';
    }
  }

  const withoutComments = textBefore.replace(/\/\/[^\n]*/g, '');
  if (/=\s*$/.test(withoutComments) && depth > 0) {
    const attrMatch = withoutComments.match(/(\w+)\s*=\s*$/);
    if (attrMatch) {
      const componentName = Object.keys(alloySchema).find((name) =>
        currentBlockHeader.startsWith(name),
      );
      const attr = componentName
        ? alloySchema[componentName].attributes?.find((a) => a.name === attrMatch[1])
        : undefined;
      if (attr?.values)
        return {
          from: ctx.pos,
          options: attr.values.map((value) => ({ label: `"${value}"`, type: 'enum' })),
        };
      if (attr?.type === 'bool') {
        return {
          from: ctx.pos,
          options: [
            { label: 'true', type: 'keyword' },
            { label: 'false', type: 'keyword' },
          ],
        };
      }
      if (attr && /interval|timeout|duration|max_wait_time/.test(attr.name)) {
        return {
          from: ctx.pos,
          options: ['1s', '5s', '30s', '1m'].map((label) => ({ label, type: 'constant' })),
        };
      }

      const exportRefs: string[] = [];
      const exportPattern = /^\s*([\w.]+)\s+"([\w-]+)"\s*\{/gm;
      let match: RegExpExecArray | null;
      while ((match = exportPattern.exec(docText)) !== null) {
        for (const exportName of alloySchema[match[1]]?.exports ?? [])
          exportRefs.push(`${match[1]}.${match[2]}.${exportName}`);
      }
      if (exportRefs.length)
        return { from: ctx.pos, options: exportRefs.map((label) => ({ label, type: 'variable' })) };
    }
  }

  const word = ctx.matchBefore(/[\w.]+/);
  const from = word ? word.from : ctx.pos;
  if (depth === 0) {
    const options = Object.entries(alloySchema).map(([name, def]) => ({
      label: name,
      type: 'keyword',
      detail: def.doc,
      apply: def.hasLabel ? name + ' "${1:label}" {\n  ${0}\n}' : name + ' {\n  ${0}\n}',
      boost: 1,
    }));
    return { from, options, validFor: /^[\w.]*$/ };
  }

  const componentName = Object.keys(alloySchema).find((name) =>
    currentBlockHeader.startsWith(name),
  );
  if (!componentName) return null;
  const component = alloySchema[componentName];
  const blockStart = textBefore.lastIndexOf('{');
  const blockText = blockStart >= 0 ? textBefore.slice(blockStart) : '';
  const presentAttrs = new Set(Array.from(blockText.matchAll(/^\s*(\w+)\s*=/gm), (m) => m[1]));
  const attrOptions = (component.attributes ?? [])
    .filter((attr) => !presentAttrs.has(attr.name))
    .map((attr) => ({
      label: attr.name,
      type: 'property',
      detail: attr.doc,
      boost: attr.required ? 2 : 1,
      apply: `${attr.name} = `,
    }));
  const blockOptions = (component.blocks ?? []).map((block) => ({
    label: block.name,
    type: 'class',
    apply: block.name + ' {\n  ${0}\n}',
  }));
  return { from, options: [...attrOptions, ...blockOptions], validFor: /^\w*$/ };
}

export { autocompletion };
