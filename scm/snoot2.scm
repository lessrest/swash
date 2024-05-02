;; What are the axioms of this thing?
;;
;; We want to implement structured concurrency,
;; in a way that can interoperate with promises.
;;
;; That just means there's a hierarchy of task scopes,
;; and we can't leave a task scope until all the tasks
;; in it are done.
;;
;; When a task scope returns, it must somehow join all
;; its subtasks.  Java has the StructuredTaskScope<T> class
;; for example, with a try (var scope = new StructuredTaskScope())
;; block that demands .join() be called on it before the block
;; can exit.  I believe it's known as a nursery in other systems
;; like Python's trio.
;;
;; We use a prompt to implement this.

(use-modules (ice-9 match))

(define task-tag (make-prompt-tag 'task))

(define (info . items)
  (abort-to-prompt task-tag 'display)
  (newline)
  (display items)
  (newline))

(define (nest id continuation)
  (call-with-prompt task-tag
	continuation
	(match-lambda*
	  ((k 'display)
	   (begin
		 (abort-to-prompt task-tag 'display)
		 (display id)
		 (display " ")
		 (nest id k)))
	  ((k 'suspend)
	   ;; this is really the core of the thing...
	   ;; we're going to save the continuation...
	   ;; and then call it later...
	   ;; tricky business...
		   ))))

(define (root continuation)
  (call-with-prompt task-tag 
	continuation
	(match-lambda*
	  ((k 'display)
	   (display "% ")
	   (root k)))))

(root 
	(lambda ()
	  (nest 'foo (lambda ()
				   (info "hello")
				   (nest 'bar (lambda ()
								(info "world")))))))
