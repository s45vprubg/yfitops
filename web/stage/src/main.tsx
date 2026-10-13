import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import StageApp from "./App";
import PublicLanding from "./PublicLanding";
import ErrorBoundary from "./ErrorBoundary";
import "./index.css";

// One build, two routes: /stage is the real (login-gated) stage, everything
// else (the site root) is the public, unauthenticated landing page. No
// router needed for two static paths — a single pathname check is enough,
// and it decides which component tree mounts, so PublicLanding never pulls
// in useGame()/any privileged connection at all.
const isStage = window.location.pathname.startsWith("/stage");

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ErrorBoundary>
      {isStage ? <StageApp /> : <PublicLanding />}
    </ErrorBoundary>
  </StrictMode>,
);
