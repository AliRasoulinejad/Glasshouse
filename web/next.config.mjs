// Static export: every page is plain HTML. Sign-in and labs live in the Go
// gateway on the same origin, never in Next.js.
export default {
  output: 'export',
  trailingSlash: true,
  images: { unoptimized: true },
};
