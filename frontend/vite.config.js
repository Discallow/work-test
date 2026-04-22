import react from "@vitejs/plugin-react";

export default {
  plugins: [react()],
  server: {
    host: "0.0.0.0",
    port: 5173,
    proxy: {
      "/employees": {
        target: "http://backend:8080",
        changeOrigin: true,
      },
      "/health": {
        target: "http://backend:8080",
        changeOrigin: true,
      },
    },
  },
};