import type { Metadata, Viewport } from 'next';
import { Inter, Space_Grotesk, Outfit } from 'next/font/google';
import './globals.css';
import { AuthProvider } from '@/components/auth/AuthProvider';

// display: 'optional' instead of 'swap' -- 'swap' guarantees a visible
// font-swap flash once the webfont loads (fallback -> webfont mid-session,
// the "font changes while loading" symptom). 'optional' gives the browser a
// short window (~100ms) to use the webfont if already cached; if it's not
// ready in time, that render keeps the fallback for the rest of the session
// instead of swapping later. First visit may briefly show the fallback
// font, but never flashes -- and next/font's static export self-hosts these
// files, so repeat visits hit cache and paint the real font immediately.
const inter = Inter({
  subsets: ['latin'],
  variable: '--font-inter',
  display: 'optional',
});

const spaceGrotesk = Space_Grotesk({
  subsets: ['latin'],
  variable: '--font-headline',
  display: 'optional',
});

const outfit = Outfit({
  subsets: ['latin'],
  variable: '--font-body',
  display: 'optional',
});

export const metadata: Metadata = {
  title: 'TTOBAK - AI Meeting Assistant',
  description: 'Record, transcribe, and summarize your meetings with AI',
  manifest: '/manifest.json',
  icons: {
    icon: [
      { url: '/favicon.ico', sizes: '48x48' },
      { url: '/favicon.svg', type: 'image/svg+xml' },
    ],
    apple: '/apple-touch-icon.png',
  },
};

export const viewport: Viewport = {
  width: 'device-width',
  initialScale: 1,
  maximumScale: 1,
  userScalable: false,
  themeColor: '#3211d4',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" className={`${inter.variable} ${spaceGrotesk.variable} ${outfit.variable}`} suppressHydrationWarning>
      <head>
        <script
          dangerouslySetInnerHTML={{
            __html: `(function(){try{var t=localStorage.getItem('theme');if(t==='dark'||(!t&&matchMedia('(prefers-color-scheme:dark)').matches))document.documentElement.classList.add('dark')}catch(e){}})()`,
          }}
        />
        {/* Recover from stale JS chunks after deployment — force full reload */}
        <script
          dangerouslySetInnerHTML={{
            __html: `(function(){var R=0;function reload(){if(R)return;R=1;window.location.reload()}window.addEventListener('error',function(e){if(e.message&&(/ChunkLoadError|Loading chunk|Failed to fetch dynamically imported module/.test(e.message)))reload()});window.addEventListener('unhandledrejection',function(e){var r=e.reason;if(r&&(r.name==='ChunkLoadError'||(/Loading chunk|dynamically imported module/.test(r.message||''))))reload()})})()`,
          }}
        />
        <link
          href="https://fonts.googleapis.com/css2?family=Material+Symbols+Outlined:opsz,wght,FILL,GRAD@20..48,100..700,0..1,-50..200&display=swap"
          rel="stylesheet"
        />
      </head>
      <body className="font-sans antialiased bg-background-light dark:bg-background-dark text-slate-900 dark:text-slate-100">
        <AuthProvider>{children}</AuthProvider>
      </body>
    </html>
  );
}
