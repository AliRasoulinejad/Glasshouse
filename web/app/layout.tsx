import type { ReactNode } from 'react';
import './globals.css';

export const metadata = { title: 'Glasshouse', description: 'The real internals of running systems, read from real instances.' };

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body>
        <header className="topbar">
          <a className="brand" href="/"><span className="pane-glyph brand-mark" aria-hidden="true" />Glasshouse</a>
        </header>
        {children}
      </body>
    </html>
  );
}
