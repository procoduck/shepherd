#!/usr/bin/env python3
"""Assemble the documentation site from content fragments + shared chrome.

The docs are many small pages sharing one sidebar, one header and one footer.
Hand-copying that chrome into every page is how a nav goes stale in exactly
one place and nobody notices, so the chrome lives here and the pages under
site/docs/ are generated from it.

Output is COMMITTED, so GitHub Pages keeps serving a plain static directory
with no build step (.github/workflows/pages.yml hands site/ straight to the
Pages action). `make docs` regenerates; `make check-docs-drift` fails when the
committed output no longer matches, the same bargain the repo already makes
for protobuf and sqlc output.

Content fragments are HTML, not Markdown: the pages use the site's own
components (code blocks with copy buttons, note callouts, tables) and a
Markdown layer would only be something to fight.
"""

import html
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CONTENT = os.path.join(ROOT, "scripts", "docs-content")
OUT = os.path.join(ROOT, "site", "docs")

# The sidebar, in order. Each page: (slug, nav title, <title>, meta description).
# A page's category is the group it appears under here — there is no separate
# category field to fall out of sync with the nav.
NAV = [
    ("Get started", [
        ("quickstart", "Quickstart", "Quickstart",
         "Run Shepherd locally and serve a first pipeline to a Grafana Alloy collector in about five minutes."),
    ]),
    ("About", [
        ("overview", "What Shepherd is", "What Shepherd is",
         "What Shepherd does, how collectors reach it, and what it is not."),
        ("roles", "Roles and access", "Roles and access",
         "Who can do what in Shepherd, and the two independent ways a person gets a role."),
    ]),
    ("Install", [
        ("requirements", "Requirements", "Requirements",
         "What Shepherd needs to run: PostgreSQL, an Alloy version, and the ports it listens on."),
        ("local-development", "Local development", "Local development",
         "Run the full stack locally with Docker, seeded and ready to click through."),
        ("kubernetes", "Kubernetes (Helm)", "Kubernetes (Helm)",
         "Install the published Helm chart from its OCI registry, with no clone and no repo to add."),
        ("configuration", "Configuration", "Configuration",
         "Every setting Shepherd reads, which are required, and which must never be rotated."),
    ]),
    ("Fleet", [
        ("agent-tokens", "Agent tokens", "Agent tokens",
         "Mint the credential an Alloy collector uses to fetch its configuration."),
        ("collectors", "Connect a collector", "Connect a collector",
         "Point Alloy at Shepherd with remotecfg, and claim the cluster it reports."),
    ]),
    ("Pipelines", [
        ("authoring", "Authoring pipelines", "Authoring pipelines",
         "Three ways to write a pipeline — a wizard, the visual builder, or raw Alloy — all landing in one merge engine."),
        ("matchers", "Matchers and validation", "Matchers and validation",
         "How Shepherd decides which collectors receive a pipeline, and what it refuses to serve."),
        ("gitops", "GitOps", "GitOps",
         "Keep pipelines in a git repository and let Shepherd poll it."),
    ]),
    ("Access control", [
        ("single-sign-on", "Single sign-on", "Single sign-on",
         "Connect any spec-compliant OIDC provider, from the chart or from the admin UI."),
        ("users-and-teams", "Users and teams", "Users and teams",
         "Local accounts, org roles and team membership — with or without an identity provider."),
    ]),
    ("Sandbox", [
        ("simulation", "Sandbox simulation", "Sandbox simulation",
         "Run a candidate pipeline against synthetic telemetry in a contained sandbox before it reaches a collector."),
    ]),
    ("Reference", [
        ("resources", "Further reading", "Further reading",
         "The specification, chart values, changelog and source."),
    ]),
]

# URLs that existed before the docs were split, mapped to where their content
# went. They are still linked from elsewhere on the internet, and GitHub Pages
# has no server-side redirects, so each one is published as a stub that
# forwards. Removing an entry here is deliberately a decision to break a link.
REDIRECTS = {
    "getting-started": ("index.html", "Documentation"),
}

REDIRECT_PAGE = """<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="0; url={target}">
<link rel="canonical" href="{target}">
<meta name="robots" content="noindex">
<title>Moved &mdash; Shepherd docs</title>
</head>
<body>
<p>This page moved. Continue to <a href="{target}">{label}</a>.</p>
</body>
</html>
"""

HEAD = """<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{title} &mdash; Shepherd docs</title>
<meta name="description" content="{description}">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;650;700&amp;family=JetBrains+Mono:wght@400;500;600&amp;display=swap" rel="stylesheet">
<link rel="stylesheet" href="../styles.css">
</head>
<body>

<nav class="nav">
  <div class="wrap">
    <a class="brand" href="../index.html">
      <svg width="22" height="22" viewBox="0 0 24 24" fill="none" aria-hidden="true">
        <path d="M12 2.5 4 6v6c0 5.2 3.4 9.4 8 10.5 4.6-1.1 8-5.3 8-10.5V6l-8-3.5Z" stroke="var(--accent)" stroke-width="1.6" stroke-linejoin="round"/>
        <path d="M8.5 12.2 11 14.7l4.8-5" stroke="var(--accent)" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>
      </svg>
      Shepherd
    </a>
    <div class="nav-links">
      <a href="../index.html#features">Features</a>
      <a href="../index.html#architecture">Architecture</a>
      <a href="index.html">Docs</a>
      <a class="btn btn-outline" href="https://github.com/procoduck/shepherd">GitHub</a>
    </div>
  </div>
</nav>
"""

FOOT = """
<footer>
  <div class="wrap">
    <div class="foot-left">
      <span class="pill">{version}</span>
      <span>self-hosted Grafana Alloy fleet manager</span>
    </div>
    <div class="foot-links">
      <a href="https://github.com/procoduck/shepherd">GitHub</a>
      <a href="index.html">Docs</a>
      <a href="../index.html">Home</a>
    </div>
  </div>
</footer>

<script src="../docs.js" defer></script>
</body>
</html>
"""


def app_version():
    """The released app version, read from the chart so it has one source."""
    path = os.path.join(ROOT, "deploy", "helm", "shepherd", "Chart.yaml")
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            if line.startswith("appVersion:"):
                return "v" + line.split(":", 1)[1].strip().strip('"')
    raise SystemExit("build-docs: could not read appVersion from Chart.yaml")


def flat_pages():
    out = []
    for category, pages in NAV:
        for slug, nav_title, title, desc in pages:
            out.append((category, slug, nav_title, title, desc))
    return out


def sidebar(active_slug):
    rows = ['<aside class="docs-nav" id="docs-nav">',
            '  <p class="docs-nav-label">Documentation</p>']
    for category, pages in NAV:
        rows.append('  <p class="docs-nav-cat">%s</p>' % html.escape(category))
        rows.append('  <ul>')
        for slug, nav_title, _, _ in pages:
            cls = ' class="active"' if slug == active_slug else ""
            aria = ' aria-current="page"' if slug == active_slug else ""
            rows.append('    <li><a href="%s.html"%s%s>%s</a></li>'
                        % (slug, cls, aria, html.escape(nav_title)))
        rows.append('  </ul>')
    rows.append("</aside>")
    return "\n".join(rows)


HEADING_RE = re.compile(r'<h([23])\s+id="([^"]+)">(.*?)</h\1>', re.S)


def page_toc(fragment):
    """Right-hand 'On this page', built from the fragment's own h2/h3 ids.

    Derived rather than hand-listed: a TOC that has to be updated by hand is a
    TOC that silently stops matching the page.
    """
    items = []
    for level, anchor, text in HEADING_RE.findall(fragment):
        label = re.sub(r"<[^>]+>", "", text).strip()
        cls = ' class="sub"' if level == "3" else ""
        items.append('        <li%s><a href="#%s">%s</a></li>' % (cls, anchor, label))
    if not items:
        return ""
    return ('    <aside class="docs-toc">\n'
            '      <p class="toc-label">On this page</p>\n'
            '      <ul>\n' + "\n".join(items) + "\n"
            '      </ul>\n'
            '    </aside>\n')


def render(category, slug, title, desc, fragment, prev_page, next_page, version):
    parts = [HEAD.format(title=html.escape(title), description=html.escape(desc))]
    parts.append('\n<div class="docs-body">\n  <div class="wrap docs-shell">\n')
    parts.append(sidebar(slug))
    parts.append('\n\n    <div class="docs-main">\n')
    parts.append('      <nav class="crumbs" aria-label="Breadcrumb">'
                 '<a href="index.html">Docs</a>'
                 '<span aria-hidden="true">/</span>'
                 '<span>%s</span>'
                 '<span aria-hidden="true">/</span>'
                 '<span class="crumb-here">%s</span></nav>\n'
                 % (html.escape(category), html.escape(title)))
    parts.append('      <div class="docs-content">\n')
    parts.append(fragment.strip() + "\n")
    if prev_page or next_page:
        parts.append('        <nav class="page-nav" aria-label="Pagination">\n')
        if prev_page:
            parts.append('          <a class="page-nav-prev" href="%s.html">'
                         '<span>Previous</span><strong>%s</strong></a>\n'
                         % (prev_page[0], html.escape(prev_page[1])))
        else:
            parts.append('          <span></span>\n')
        if next_page:
            parts.append('          <a class="page-nav-next" href="%s.html">'
                         '<span>Next</span><strong>%s</strong></a>\n'
                         % (next_page[0], html.escape(next_page[1])))
        parts.append('        </nav>\n')
    parts.append('      </div>\n')
    parts.append('    </div>\n\n')
    parts.append(page_toc(fragment))
    parts.append('  </div>\n</div>\n')
    parts.append(FOOT.format(version=version))
    return "".join(parts)


def render_index(version):
    """The docs landing page: one card per category, linking into it."""
    cards = []
    for category, pages in NAV:
        first = pages[0][0]
        items = "".join(
            '<li><a href="%s.html">%s</a></li>' % (s, html.escape(n))
            for s, n, _, _ in pages)
        cards.append(
            '        <article class="doc-card">\n'
            '          <h2><a href="%s.html">%s</a></h2>\n'
            '          <ul>%s</ul>\n'
            '        </article>' % (first, html.escape(category), items))

    body = (
        '      <div class="docs-content docs-index">\n'
        '        <h1>Shepherd documentation</h1>\n'
        '        <p class="lede-sm">Shepherd serves centralised pipeline configuration to a fleet of\n'
        '        Grafana Alloy collectors over <code>remotecfg</code>. These pages cover installing it,\n'
        '        connecting collectors, authoring pipelines, and deciding who may change what.</p>\n'
        '        <p class="note"><strong>New here?</strong> Start with the\n'
        '        <a href="quickstart.html">Quickstart</a> &mdash; a working instance serving a pipeline\n'
        '        to a collector, on your own machine, in about five minutes.</p>\n'
        '        <div class="doc-cards">\n' + "\n".join(cards) + "\n"
        '        </div>\n'
        '      </div>\n')

    parts = [HEAD.format(title="Documentation",
                         description="Install Shepherd, connect Grafana Alloy collectors, "
                                     "author pipelines, and control who can change what.")]
    parts.append('\n<div class="docs-body">\n  <div class="wrap docs-shell docs-shell-wide">\n')
    parts.append(sidebar("index"))
    parts.append('\n\n    <div class="docs-main">\n')
    parts.append(body)
    parts.append('    </div>\n')
    parts.append('  </div>\n</div>\n')
    parts.append(FOOT.format(version=version))
    return "".join(parts)


def main():
    version = app_version()
    pages = flat_pages()
    os.makedirs(OUT, exist_ok=True)

    written = {"index.html"}
    with open(os.path.join(OUT, "index.html"), "w", encoding="utf-8") as fh:
        fh.write(render_index(version))

    for i, (category, slug, nav_title, title, desc) in enumerate(pages):
        src = os.path.join(CONTENT, slug + ".html")
        if not os.path.exists(src):
            raise SystemExit("build-docs: missing content fragment %s" % src)
        with open(src, encoding="utf-8") as fh:
            fragment = fh.read()
        prev_page = (pages[i - 1][1], pages[i - 1][2]) if i > 0 else None
        next_page = (pages[i + 1][1], pages[i + 1][2]) if i + 1 < len(pages) else None
        out_name = slug + ".html"
        with open(os.path.join(OUT, out_name), "w", encoding="utf-8") as fh:
            fh.write(render(category, slug, title, desc, fragment,
                            prev_page, next_page, version))
        written.add(out_name)

    for slug, (target, label) in REDIRECTS.items():
        name = slug + ".html"
        with open(os.path.join(OUT, name), "w", encoding="utf-8") as fh:
            fh.write(REDIRECT_PAGE.format(target=target, label=label))
        written.add(name)

    # A page removed from NAV must stop being published, not linger unreachable.
    stale = [f for f in os.listdir(OUT)
             if f.endswith(".html") and f not in written]
    for f in stale:
        os.remove(os.path.join(OUT, f))
        print("build-docs: removed stale page %s" % f)

    print("build-docs: wrote %d pages to site/docs/" % len(written))
    return 0


if __name__ == "__main__":
    sys.exit(main())
