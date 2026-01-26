// Type declarations for Vite-specific import suffixes

declare module '*?raw' {
  const content: string;
  export default content;
}
