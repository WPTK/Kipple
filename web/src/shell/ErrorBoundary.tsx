import { Component, type ErrorInfo, type ReactNode } from "react";
import { useLocation } from "react-router";
import { ChunkLoadError, resetFailedScreens, retryFailedScreens } from "@/lib/lazyScreen";
import { StatusBlock } from "@/screens/ListPane";
import { Button } from "@/ui/button";

interface State {
  failed: boolean;
  /** The failure was a screen that could not be downloaded, not a rendering error. */
  chunk: boolean;
}

interface Props {
  children: ReactNode;
  /** A change of this value (the address) clears the error: going somewhere else must not keep the message up. */
  resetKey?: string;
}

/**
 * The last line: an error thrown while rendering shows a message with a way out instead of a blank page. "Try
 * again" re-renders in place (a passing glitch), and for a screen that could not be downloaded it downloads it
 * again (lib/lazyScreen.ts); "Go to Unread" leaves the address that may have caused it and loads the app fresh.
 * Moving to another address clears the message.
 */
export class ErrorBoundary extends Component<Props, State> {
  override state: State = { failed: false, chunk: false };

  static getDerivedStateFromError(error: unknown): State {
    return { failed: true, chunk: error instanceof ChunkLoadError };
  }

  override componentDidCatch(error: unknown, info: ErrorInfo): void {
    console.error("Kipple hit an error while drawing the page", error, info.componentStack);
  }

  override componentDidUpdate(prev: Props): void {
    if (this.state.failed && prev.resetKey !== this.props.resetKey) {
      // A screen that failed to download is tried afresh when it is next opened, not only through "Try again".
      resetFailedScreens();
      this.setState({ failed: false, chunk: false });
    }
  }

  private tryAgain = (): void => {
    if (this.state.chunk) retryFailedScreens();
    this.setState({ failed: false, chunk: false });
  };

  override render(): ReactNode {
    if (!this.state.failed) return this.props.children;
    return (
      <StatusBlock role="alert" title="Something went wrong" body="Kipple couldn't show this page. Try again, or go back to Unread.">
        <div className="flex flex-wrap justify-center gap-2">
          <Button onClick={this.tryAgain}>Try again</Button>
          <Button onClick={() => window.location.assign("/l/unread")}>
            Go to Unread
          </Button>
        </div>
      </StatusBlock>
    );
  }
}

/** The boundary for the routed app: it clears itself when the address changes. Must sit inside the router. */
export function RoutedErrorBoundary({ children }: { children: ReactNode }) {
  const { pathname } = useLocation();
  return <ErrorBoundary resetKey={pathname}>{children}</ErrorBoundary>;
}
