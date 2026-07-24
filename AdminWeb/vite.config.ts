import {defineConfig, loadEnv} from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig(({mode}) => {
  const env = loadEnv(mode, process.cwd(), "");
  const controlPlane = env.ADMIN_CONTROL_PLANE_URL || "http://127.0.0.1:8080";

  return {
    plugins: [react()],
    server: {
      host: "127.0.0.1",
      port: 5173,
      strictPort: true,
      proxy: {
        "/v1/admin": {
          target: controlPlane,
          changeOrigin: true,
          secure: false
        }
      }
    },
    preview: {
      host: "127.0.0.1",
      port: 4173,
      strictPort: true
    },
    build: {
      outDir: "dist",
      sourcemap: true
    }
  };
});
