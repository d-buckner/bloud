/**
 * HTTP Client - Base utilities for API communication
 *
 * Handles common concerns: response checking, error handling, JSON parsing.
 */

export interface HttpError {
	status: number;
	message: string;
}

export interface RequestOptions extends Omit<RequestInit, 'body'> {
	body?: Record<string, any>;
}

/**
 * Make an HTTP request with standard error handling
 *
 * @throws HttpError on non-2xx responses
 */
export async function request<T>(url: string, options: RequestOptions = {}): Promise<T> {
	const { body, headers, ...rest } = options;

	const res = await fetch(url, {
		...rest,
		headers: {
			...(body !== undefined && { 'Content-Type': 'application/json' }),
			...headers
		},
		body: body !== undefined ? JSON.stringify(body) : undefined
	});

	if (!res.ok) {
		const data = await res.json().catch(() => ({}));
		const error: HttpError = {
			status: res.status,
			message: data.error || `Request failed: ${res.status}`
		};
		throw error;
	}

	// Handle empty responses
	const text = await res.text();
	if (!text) return undefined as T;

	return JSON.parse(text) as T;
}

/**
 * Make a GET request
 */
export function get<T>(url: string, options?: Omit<RequestOptions, 'method'>): Promise<T> {
	return request<T>(url, { ...options, method: 'GET' });
}

/**
 * Make a POST request
 */
export function post<T>(url: string, body?: Record<string, any>, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<T> {
	return request<T>(url, { ...options, method: 'POST', body });
}

/**
 * Make a PUT request
 */
export function put<T>(url: string, body?: Record<string, any>, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<T> {
	return request<T>(url, { ...options, method: 'PUT', body });
}

/**
 * Make a PATCH request
 */
export function patch<T>(url: string, body?: Record<string, any>, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<T> {
	return request<T>(url, { ...options, method: 'PATCH', body });
}

/**
 * Make a DELETE request
 */
export function del<T>(url: string, options?: Omit<RequestOptions, 'method'>): Promise<T> {
	return request<T>(url, { ...options, method: 'DELETE' });
}

/**
 * Check if an error is an HttpError with a specific status
 */
export function isHttpError(error: unknown, status?: number): error is HttpError {
	if (!error || typeof error !== 'object') return false;
	const httpError = error as HttpError;
	if (typeof httpError.status !== 'number') return false;
	if (status !== undefined && httpError.status !== status) return false;
	return true;
}

/**
 * Check if error is unauthorized (401)
 */
export function isUnauthorized(error: unknown): boolean {
	return isHttpError(error, 401);
}
