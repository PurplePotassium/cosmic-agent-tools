import React, { createContext, useCallback, useContext, useRef, useState } from 'react';

const ToastContext = createContext(() => {});

export function useToast() {
  return useContext(ToastContext);
}

export function ToastProvider({ children }) {
  const [toasts, setToasts] = useState([]);
  const idRef = useRef(0);

  const toast = useCallback((msg, kind) => {
    const id = ++idRef.current;
    setToasts((cur) => [...cur, { id, msg, kind }]);
    setTimeout(() => {
      setToasts((cur) => cur.filter((t) => t.id !== id));
    }, 3200);
  }, []);

  return (
    <ToastContext.Provider value={toast}>
      {children}
      <div id="toast">
        {toasts.map((t) => (
          <div key={t.id} className={'toast ' + (t.kind || '')}>
            {t.msg}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
