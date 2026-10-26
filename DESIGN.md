# Historical login concept

Historical visual mockup, superseded by the implemented UI. The original standalone
HTML concept used neon cyan/purple, glass/glow effects, social-login buttons and a
Create Account link. Those elements are not current product requirements.

Current tokens use a unified indigo/violet brand in
`frontend/src/app/globals.css`. Current Cognito UI supports administrator-created
or scoped invitation-created identities, login, password challenges and reset;
self-signup remains forbidden. The Account API supports a designated initial
Owner and optional creator AM membership (ADR-055). The matching Owner picker,
checkbox and creation receipt UI remain pending in companion PR #479.
Do not reintroduce mockup auth flows or CDN Tailwind configuration into the app.

See [current UI reference](docs/DESIGN-SPEC.md) and
[frontend guidance](frontend/CLAUDE.md). Git history retains the original mockup.
