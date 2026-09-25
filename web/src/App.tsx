import { useEffect, useState } from "react";

type Health = "checking" | "ok" | "error";

export default function App() {
  const [health, setHealth] = useState<Health>("checking");

  useEffect(() => {
    let cancelled = false;
    fetch("/healthz")
      .then((res) => {
        if (!cancelled) setHealth(res.ok ? "ok" : "error");
      })
      .catch(() => {
        if (!cancelled) setHealth("error");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <main>
      <h1>Kipple</h1>
      <p>API: {health}</p>
    </main>
  );
}
