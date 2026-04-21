<!DOCTYPE html>

<html class="dark" lang="en"><head>
<meta charset="utf-8"/>
<meta content="width=device-width, initial-scale=1.0" name="viewport"/>
<link href="https://fonts.googleapis.com/css2?family=Space+Grotesk:wght@300;400;500;600;700&amp;family=Outfit:wght@300;400;500;600&amp;family=Inter:wght@400;500;600&amp;display=swap" rel="stylesheet"/>
<link href="https://fonts.googleapis.com/css2?family=Material+Symbols+Outlined:wght,FILL@100..700,0..1&amp;display=swap" rel="stylesheet"/>
<link href="https://fonts.googleapis.com/css2?family=Material+Symbols+Outlined:wght,FILL@100..700,0..1&amp;display=swap" rel="stylesheet"/>
<script src="https://cdn.tailwindcss.com?plugins=forms,container-queries"></script>
<script id="tailwind-config">
        tailwind.config = {
            darkMode: "class",
            theme: {
                extend: {
                    "colors": {
                        "primary-fixed": "#9cf0ff",
                        "on-secondary-fixed-variant": "#7000a8",
                        "inverse-primary": "#006875",
                        "inverse-on-surface": "#303036",
                        "error-container": "#93000a",
                        "primary": "#c3f5ff",
                        "surface-container-high": "#2a292f",
                        "surface-container-low": "#1b1b20",
                        "primary-container": "#00e5ff",
                        "background": "#09090E",
                        "tertiary": "#f0eaff",
                        "on-primary-fixed": "#001f24",
                        "on-error": "#690005",
                        "surface-bright": "#39383e",
                        "outline": "#849396",
                        "on-secondary-container": "#f7e1ff",
                        "surface-container": "#1f1f25",
                        "on-secondary": "#4e0078",
                        "on-tertiary-fixed-variant": "#4300d9",
                        "on-primary-fixed-variant": "#004f58",
                        "surface": "#131318",
                        "surface-container-highest": "#35343a",
                        "secondary-container": "#a100f0",
                        "on-surface": "#e4e1e9",
                        "inverse-surface": "#e4e1e9",
                        "secondary-fixed": "#f4d9ff",
                        "on-background": "#e4e1e9",
                        "on-primary-container": "#00626e",
                        "tertiary-container": "#d3caff",
                        "error": "#ffb4ab",
                        "tertiary-fixed": "#e5deff",
                        "surface-dim": "#131318",
                        "surface-tint": "#00daf3",
                        "primary-fixed-dim": "#00daf3",
                        "surface-container-lowest": "#0e0e13",
                        "secondary": "#e5b5ff",
                        "on-tertiary-container": "#562ded",
                        "on-primary": "#00363d",
                        "secondary-fixed-dim": "#e5b5ff",
                        "tertiary-fixed-dim": "#c8bfff",
                        "on-error-container": "#ffdad6",
                        "on-tertiary-fixed": "#1a0063",
                        "on-surface-variant": "#bac9cc",
                        "on-secondary-fixed": "#30004b",
                        "outline-variant": "#3b494c",
                        "surface-variant": "#35343a",
                        "on-tertiary": "#2e009c"
                    },
                    "borderRadius": {
                        "DEFAULT": "0.125rem",
                        "lg": "0.25rem",
                        "xl": "0.5rem",
                        "full": "0.75rem"
                    },
                    "fontFamily": {
                        "headline": ["Space Grotesk"],
                        "body": ["Outfit"],
                        "label": ["Inter"]
                    }
                },
            }
        }
    </script>
<style>
        body {
            background-color: #09090E;
            font-family: 'Outfit', sans-serif;
            color: #e4e1e9;
        }
        .neon-cyan-glow {
            box-shadow: 0 0 20px rgba(0, 229, 255, 0.4);
        }
        .neon-purple-glow {
            box-shadow: 0 0 20px rgba(176, 38, 255, 0.3);
        }
        .glass-panel {
            background: rgba(255, 255, 255, 0.03);
            backdrop-filter: blur(24px);
            border: 1px solid rgba(255, 255, 255, 0.08);
            background-image: linear-gradient(180deg, rgba(255, 255, 255, 0.08) 0%, rgba(255, 255, 255, 0) 100%);
        }
        .ambient-glow-cyan {
            background: radial-gradient(circle, rgba(0, 229, 255, 0.12) 0%, rgba(0, 229, 255, 0) 70%);
        }
        .ambient-glow-purple {
            background: radial-gradient(circle, rgba(176, 38, 255, 0.1) 0%, rgba(176, 38, 255, 0) 70%);
        }
    </style>
</head>
<body class="min-h-screen flex items-center justify-center overflow-hidden relative">
<!-- Ambient Background Lighting -->
<div class="absolute -top-40 -left-40 w-128 h-128 ambient-glow-cyan blur-[120px] pointer-events-none"></div>
<div class="absolute -bottom-40 -right-40 w-128 h-128 ambient-glow-purple blur-[120px] pointer-events-none"></div>
<!-- Login Container -->
<main class="relative z-10 w-full max-w-md px-6 py-12">
<!-- Logo Section -->
<div class="text-center mb-12">
<h1 class="font-headline text-4xl md:text-5xl font-bold tracking-tight text-primary-container drop-shadow-[0_0_12px_rgba(0,229,255,0.6)]">
                TTOBAK Assist
            </h1>
<p class="font-body text-on-surface-variant mt-3 text-lg opacity-80">Intelligence redefined for the obsidian era.</p>
</div>
<!-- Glassmorphism Panel -->
<div class="glass-panel rounded-full p-8 md:p-10">
<form class="space-y-6">
<!-- Email Field -->
<div class="space-y-2">
<label class="font-headline text-sm font-medium text-primary-container ml-1 uppercase tracking-widest" for="email">Email Address</label>
<div class="relative group">
<span class="material-symbols-outlined absolute left-4 top-1/2 -translate-y-1/2 text-on-surface-variant group-focus-within:text-primary-container transition-colors">alternate_email</span>
<input class="w-full bg-surface-container-lowest border border-white/10 rounded-xl py-4 pl-12 pr-4 text-white placeholder-on-surface-variant/40 focus:ring-2 focus:ring-primary-container/20 focus:border-primary-container outline-none transition-all font-label" id="email" placeholder="name@company.com" type="email"/>
</div>
</div>
<!-- Password Field -->
<div class="space-y-2">
<div class="flex justify-between items-center px-1">
<label class="font-headline text-sm font-medium text-primary-container uppercase tracking-widest" for="password">Password</label>
<a class="font-body text-xs text-secondary hover:text-on-secondary-container transition-colors" href="#">Forgot Password?</a>
</div>
<div class="relative group">
<span class="material-symbols-outlined absolute left-4 top-1/2 -translate-y-1/2 text-on-surface-variant group-focus-within:text-primary-container transition-colors">lock</span>
<input class="w-full bg-surface-container-lowest border border-white/10 rounded-xl py-4 pl-12 pr-12 text-white placeholder-on-surface-variant/40 focus:ring-2 focus:ring-primary-container/20 focus:border-primary-container outline-none transition-all font-label" id="password" placeholder="••••••••" type="password"/>
<button class="absolute right-4 top-1/2 -translate-y-1/2 text-on-surface-variant hover:text-white" type="button">
<span class="material-symbols-outlined text-xl">visibility</span>
</button>
</div>
</div>
<!-- Primary Action -->
<button class="w-full bg-primary-container text-on-primary-fixed font-headline font-bold py-4 rounded-xl neon-cyan-glow hover:scale-[1.02] active:scale-[0.98] transition-all duration-300 tracking-tight text-lg" type="submit">
                    Log In
                </button>
<!-- Divider -->
<div class="relative flex items-center py-4">
<div class="flex-grow border-t border-white/5"></div>
<span class="flex-shrink mx-4 text-on-surface-variant text-xs uppercase tracking-widest opacity-50 font-label">or continue with</span>
<div class="flex-grow border-t border-white/5"></div>
</div>
<!-- Social Logins -->
<div class="grid grid-cols-2 gap-4">
<button class="flex items-center justify-center gap-2 py-3 rounded-xl border border-white/10 bg-white/5 hover:bg-white/10 transition-all font-body text-sm" type="button">
<img alt="Google" class="w-5 h-5" data-alt="minimalist google logo with clean white spacing on dark background" src="https://lh3.googleusercontent.com/aida-public/AB6AXuCHGQ8rVMfYS5l7-GbamP6iHYd6FXVTV-lEdiNkQYWP6Y7fwhEpVxkNMhmDA2ArXn0mYBuTGXSUVgLQvMQXSFCupWl10lQ2U2yhRVajulreA8V1METOEpwTYG1Xz88LOedgHo4iaLKqUTb2jnOClYW_Jsx70KCrAK0wljoXSelAQXo3_nHqCiEt3jGFObPDq-sy3xpe3zDsKrT5SGAhIh22RgZvEuG-n331-4APvDImmkI3cYnsGvbQ0MloQu8-BeyPQVfKxoRqh9IM"/>
<span>Google</span>
</button>
<button class="flex items-center justify-center gap-2 py-3 rounded-xl border border-white/10 bg-white/5 hover:bg-white/10 transition-all font-body text-sm" type="button">
<span class="material-symbols-outlined" data-weight="fill">ios</span>
<span>Apple</span>
</button>
</div>
</form>
</div>
<!-- Footer Links -->
<div class="mt-10 text-center">
<p class="font-body text-on-surface-variant">
                New to the platform? 
                <a class="text-secondary font-semibold hover:underline underline-offset-4 decoration-2" href="#">Create Account</a>
</p>
</div>
</main>
<!-- Decorative Elements -->
<div class="absolute inset-0 z-0 pointer-events-none opacity-20 overflow-hidden">
<div class="absolute top-1/4 right-1/4 w-[40rem] h-[40rem] border border-white/5 rounded-full transform rotate-12"></div>
<div class="absolute bottom-1/4 left-1/4 w-[30rem] h-[30rem] border border-white/5 rounded-full transform -rotate-12"></div>
</div>
<!-- Background Illustration Overlay -->
<div class="absolute bottom-0 left-0 w-full h-1/3 bg-gradient-to-t from-background to-transparent z-0 opacity-50"></div>
<!-- Hero Image Placeholder (Faded Background) -->
<div class="absolute inset-0 -z-10 overflow-hidden opacity-30 grayscale contrast-125">
<div class="w-full h-full bg-cover bg-center" data-alt="abstract 3d crystalline structure in deep space with prismatic light refractions and deep indigo shadows" style="background-image: url('https://lh3.googleusercontent.com/aida-public/AB6AXuAGRhxWNiXTNjRwvRoBALp064QpOSJjLGyucphBlB9Gw12f8VyE7Sb-c5i15ouvwCvxZuWf_bs8qy1AHDI-AxT4f-fh2fMJ72A48uTDPlsQHiLXe6MZ-9N4EQN0uh1x2dcQmzb4sgWrZX_2pYTsrCCallvleky827hffyf75FrsjPfdxsagssIM5fCS-nUPCKJDqxCm09P2mCjKxb-mC8akBMvy4eWNvFI-siii6SLZocvWhCDtU_lOlMCmUg_aW0F9Ib_JWApXwMm6')"></div>
</div>
</body></html>