"""Turn deploy/helm/shepherd/values.yaml into a documentation fragment.

The chart's values are documented in the chart, in comments written next to
the thing they explain. Copying them into a docs page by hand means two texts
that describe the same key and drift apart the first time one is edited, so
this reads the file and generates the page instead. `make docs` regenerates
and check-docs-drift fails when the committed page no longer matches, which
means a values change that is not reflected in the docs fails CI.

The parser is deliberately small: it reads the indentation and the comment
block above each key rather than loading YAML, because the comments ARE the
documentation and a YAML loader throws them away.
"""

import html
import re

KEY_RE = re.compile(r"^(\s*)([A-Za-z_][A-Za-z0-9_.-]*):\s*(.*?)\s*$")
COMMENT_RE = re.compile(r"^(\s*)#\s?(.*)$")


def _paragraphs(comment_lines):
    """Split a comment block into paragraphs, marking indented ones as code.

    Several comments carry an inline example (a `secrets:` block, a snippet of
    YAML). Those are indented under a prose line, and reflowing them into a
    paragraph destroys the only thing they were showing.
    """
    paras, buf = [], []
    for line in comment_lines + [""]:
        if line.strip() == "":
            if buf:
                is_code = all(l.startswith("  ") for l in buf if l.strip())
                paras.append(("code" if is_code else "text", buf))
                buf = []
            continue
        buf.append(line)
    return paras


def _render_comment(comment_lines):
    out = []
    for kind, lines in _paragraphs(comment_lines):
        if kind == "code":
            body = "\n".join(l[2:] if l.startswith("  ") else l for l in lines)
            out.append("<pre>%s</pre>" % html.escape(body))
        else:
            text = " ".join(l.strip() for l in lines)
            text = html.escape(text)
            # `code` in a comment is meant as code.
            text = re.sub(r"`([^`]+)`", r"<code>\1</code>", text)
            out.append(text)
    return "<br>".join(out)


def parse(path):
    """Return [(section, [(key, default, description), ...]), ...].

    Section is the top-level key ("image", "config", ...); key is the full
    dotted path to a leaf. A key whose value is a nested mapping is not a row
    of its own -- its children are.
    """
    with open(path, encoding="utf-8") as fh:
        lines = fh.read().splitlines()

    rows = []          # (path, default, description)
    stack = []         # (indent, name)
    comment = []
    pending = {}       # path -> comment, for keys whose value turns out nested

    for i, raw in enumerate(lines):
        if not raw.strip():
            comment = []
            continue

        cm = COMMENT_RE.match(raw)
        if cm:
            comment.append(cm.group(2).rstrip())
            continue

        km = KEY_RE.match(raw)
        if not km:
            # list items and continuations belong to the key above
            comment = []
            continue

        indent, name, value = len(km.group(1)), km.group(2), km.group(3)
        while stack and stack[-1][0] >= indent:
            stack.pop()
        path_str = ".".join([s[1] for s in stack] + [name])

        if value == "":
            # A mapping if something below is indented further; otherwise a
            # key that was written with no value at all.
            nxt = next((l for l in lines[i + 1:] if l.strip() and not l.strip().startswith("#")), "")
            nested = bool(nxt) and (len(nxt) - len(nxt.lstrip())) > indent
            if nested:
                stack.append((indent, name))
                if comment:
                    pending[path_str] = _render_comment(comment)
                comment = []
                continue

        rows.append((path_str, value if value != "" else "~", _render_comment(comment)))
        comment = []

    # group by top-level section, in file order
    grouped, order = {}, []
    for path_str, default, desc in rows:
        section = path_str.split(".")[0]
        if section not in grouped:
            grouped[section] = []
            order.append(section)
        grouped[section].append((path_str, default, desc))
    return [(s, grouped[s], pending.get(s, "")) for s in order]


def render(path, chart_version):
    sections = parse(path)
    out = [
        "<h1>Helm values</h1>",
        '      <p>',
        '        Every value the chart accepts, generated from',
        '        <a href="https://github.com/procoduck/shepherd/blob/main/deploy/helm/shepherd/values.yaml"><code>values.yaml</code></a>',
        '        in chart <strong>%s</strong>. The descriptions are the chart&rsquo;s own comments, so this'
        % html.escape(chart_version),
        '        page cannot describe a value the chart does not have, or miss one it gained.',
        '      </p>',
        '      <p class="note">',
        '        This page is generated from the chart on <code>main</code>, which can be slightly',
        '        ahead of the newest published chart. If a value here is missing from the version you',
        '        installed, check',
        '        <a href="https://github.com/procoduck/shepherd/releases">the releases</a> &mdash; or run',
        '        <code>helm show values oci://ghcr.io/procoduck/charts/shepherd --version &lt;yours&gt;</code>,',
        '        which answers for the exact chart you have.',
        '      </p>',
        '      <p class="note">',
        '        <strong>Only two values are ever required</strong>, and neither is here: the database URL',
        '        and the encryption key live in a Secret, not in values. See',
        '        <a href="configuration.html">Configuration</a>.',
        '      </p>',
    ]
    for section, rows, section_doc in sections:
        anchor = re.sub(r"[^a-z0-9]+", "-", section.lower()).strip("-")
        out.append('      <h2 id="%s"><code>%s</code></h2>' % (anchor, html.escape(section)))
        if section_doc:
            out.append("      <p>%s</p>" % section_doc)
        out.append("      <table>")
        out.append("        <thead><tr><th>Key</th><th>Default</th><th>Description</th></tr></thead>")
        out.append("        <tbody>")
        for key, default, desc in rows:
            out.append(
                "          <tr><td><code>%s</code></td><td><code>%s</code></td><td>%s</td></tr>"
                % (html.escape(key), html.escape(default), desc or "&mdash;"))
        out.append("        </tbody>")
        out.append("      </table>")
    return "\n".join(out) + "\n"
