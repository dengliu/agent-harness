import type { Metadata } from 'next'
import './globals.css'

export const metadata: Metadata = {
  title: 'ReAct Agent',
  description: 'A Go ReAct agent on GPT-5, reasoning out loud',
}

export default function RootLayout({
  children,
}: {
  children: React.ReactNode
}) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  )
}
