import type { Metadata, Viewport } from 'next';
import './globals.css';
import { AuthProvider } from '@/components/auth/AuthProvider';
import { NativeControlBridge } from '@/components/NativeControlBridge';

export const metadata: Metadata = {
  title: 'TTOBAK - AI Meeting Assistant',
  description: 'Record, transcribe, and summarize your meetings with AI',
  manifest: '/manifest.json',
  other: {
    'material-symbols-license': '/licenses/material-symbols.txt',
  },
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
    <html lang="en" suppressHydrationWarning>
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
      </head>
      <body className="font-sans antialiased bg-background-light dark:bg-background-dark text-slate-900 dark:text-slate-100">
        <AuthProvider>
          <NativeControlBridge />
          {children}
        </AuthProvider>
      </body>
    </html>
  );
}
