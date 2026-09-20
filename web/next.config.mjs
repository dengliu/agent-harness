/** @type {import('next').NextConfig} */
const backend = process.env.AGENT_API ?? 'http://localhost:8080'

const nextConfig = {
  // Proxy the API to the Go agent so the browser talks to one origin and the
  // SSE stream is not subject to CORS preflight.
  async rewrites() {
    return [{ source: '/api/:path*', destination: `${backend}/api/:path*` }]
  },
}

export default nextConfig
